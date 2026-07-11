package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	backendplugin "github.com/steveyegge/beads/backend/plugin"
	"github.com/steveyegge/beads/internal/backend/pluginprocess"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// OpenConfigured opens the backend selected by metadata.json. Built-in and
// external providers share this single selection path. External executable
// trust is resolved only from local/user configuration or environment, never
// from committed metadata.
func OpenConfigured(ctx context.Context, beadsDir string, opts ConfiguredOpenOptions) (storage.DoltStorage, Descriptor, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, Descriptor{}, fmt.Errorf("load %s: %w (refusing to fall back to another store)", configfile.ConfigPath(beadsDir), err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	name := cfg.GetBackend()
	if name != configfile.BackendDolt {
		pluginCfg, resolveErr := configfile.ResolveBackendPluginConfig(beadsDir, name)
		if resolveErr != nil {
			return nil, Descriptor{}, resolveErr
		}
		if pluginCfg != nil {
			store, openErr := pluginprocess.Open(ctx, pluginprocess.OpenOptions{
				Config: pluginprocess.Config{
					Backend: name,
					Command: pluginCfg.Command,
					Args:    pluginCfg.Args,
				},
				BeadsDir: beadsDir,
				Database: cfg.GetDoltDatabase(),
				Branch:   "main",
				ReadOnly: opts.ReadOnly,
			})
			if openErr != nil {
				return nil, Descriptor{}, openErr
			}
			return store, Descriptor{
				Name:         name,
				External:     true,
				Capabilities: capabilitiesFromPlugin(store.Capabilities()),
			}, nil
		}
	}

	provider, err := MustLookup(name)
	if err != nil {
		if name != configfile.BackendDolt {
			return nil, Descriptor{}, fmt.Errorf("backend %q has no built-in provider and no trusted local command; run 'bd backend install %s --command <path>'", name, name)
		}
		return nil, Descriptor{}, err
	}

	database := cfg.GetDoltDatabase()
	serverMode := name == configfile.BackendDolt && cfg.IsDoltServerMode()
	proxiedMode := name == configfile.BackendDolt && cfg.IsDoltProxiedServerMode()
	if name == configfile.BackendDolt && !serverMode && !proxiedMode {
		database = sanitizeDatabaseName(database)
		if !opts.ReadOnly && database != cfg.GetDoltDatabase() {
			if err := MigrateHyphenatedDatabase(beadsDir, cfg, cfg.GetDoltDatabase(), database); err != nil {
				return nil, Descriptor{}, fmt.Errorf("auto-sanitize database name %q → %q: %w", cfg.GetDoltDatabase(), database, err)
			}
		}
	}

	store, err := provider.Open(ctx, OpenOptions{
		BeadsDir:        beadsDir,
		Database:        database,
		Branch:          "main",
		ServerMode:      serverMode,
		ProxiedServer:   proxiedMode,
		ReadOnly:        opts.ReadOnly,
		ReadOnlyCommand: opts.ReadOnlyCommand,
		LenientOpen:     opts.LenientOpen,
	})
	if err != nil {
		return nil, Descriptor{}, err
	}
	return store, Descriptor{Name: name, Capabilities: provider.Capabilities(cfg)}, nil
}

func capabilitiesFromPlugin(caps backendplugin.Capabilities) Capabilities {
	return Capabilities{
		Embedded:          caps.Embedded,
		Transactions:      caps.Transactions,
		RawSQL:            caps.RawSQL,
		Leases:            caps.Leases,
		Maintenance:       caps.Maintenance,
		Versioning:        caps.Versioning,
		Branching:         caps.Branching,
		DoltRemotes:       caps.DoltRemotes,
		ConcurrentWriters: caps.ConcurrentWriters,
	}
}

func sanitizeDatabaseName(name string) string {
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}

// MigrateHyphenatedDatabase renames a legacy embedded-Dolt database directory
// and persists the sanitized name. It is exported only across Beads' internal
// package boundary so the CLI compatibility wrapper and tests use the same
// implementation as OpenConfigured.
func MigrateHyphenatedDatabase(beadsDir string, cfg *configfile.Config, oldName, newName string) error {
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	oldDir := filepath.Join(dataDir, oldName)
	newDir := filepath.Join(dataDir, newName)

	if info, err := os.Stat(oldDir); err == nil && info.IsDir() {
		_, newErr := os.Stat(newDir)
		switch {
		case newErr == nil:
			return fmt.Errorf("cannot auto-migrate database: both %q and %q exist under %s; remove one manually and retry", oldName, newName, dataDir)
		case !os.IsNotExist(newErr):
			return fmt.Errorf("checking target directory %q: %w", newDir, newErr)
		default:
			if err := os.Rename(oldDir, newDir); err != nil {
				return fmt.Errorf("renaming database directory: %w", err)
			}
			fmt.Fprintf(os.Stderr, "bd: migrated database directory %q → %q (GH#3231)\n", oldName, newName)
		}
	}

	if cfg != nil && cfg.DoltDatabase != newName {
		cfg.DoltDatabase = newName
		if err := cfg.Save(beadsDir); err != nil {
			return fmt.Errorf("persisting sanitized database name to metadata.json: %w", err)
		}
		fmt.Fprintf(os.Stderr, "bd: updated metadata.json dolt_database %q → %q (GH#3231)\n", oldName, newName)
	}
	return nil
}
