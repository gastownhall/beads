//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func usesSQLServer() bool {
	if shouldUseGlobals() {
		if serverMode || proxiedServerMode {
			return true
		}
	} else if cmdCtx != nil && (cmdCtx.ServerMode || cmdCtx.ProxiedServerMode) {
		return true
	}
	if doltserver.IsSharedServerMode() {
		return true
	}
	return false // default: embedded
}

// isEmbeddedMode reports whether the command is using embedded Dolt storage.
func isEmbeddedMode() bool {
	return !usesSQLServer()
}

func usesProxiedServer() bool {
	if shouldUseGlobals() {
		return proxiedServerMode
	}
	return cmdCtx != nil && cmdCtx.ProxiedServerMode
}

// newDoltStore creates a storage backend from an explicit config.
// Used by bd init and PersistentPreRun.
func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	if cfg.ProxiedServer {
		// TODO: this should not be a store
		// it should be a uow provider
		return nil, fmt.Errorf("proxy server store should be uow provider")
	}
	if cfg.ServerMode {
		return dolt.New(ctx, cfg)
	}
	if cfg.ReadOnly {
		// Read-only commands must not be bricked by the #4259
		// remote-migrate gate (bd-578h9.5); server mode's ReadOnly opens
		// already skip migration entirely.
		return embeddeddolt.OpenForReadOnlyCommand(ctx, cfg.BeadsDir, cfg.Database, "main")
	}
	if cfg.LenientOpen {
		// Working-set-reconcile commands (bd dolt commit, bd vc commit) must
		// not be bricked by a pending-migration dirty-table refusal: that
		// refusal's documented recovery is exactly the commit these commands
		// run, so failing the open here would deadlock (#4566).
		return embeddeddolt.OpenForWorkingSetReconcile(ctx, cfg.BeadsDir, cfg.Database, "main")
	}
	return embeddeddolt.Open(ctx, cfg.BeadsDir, cfg.Database, "main")
}

// acquireEmbeddedLock acquires an exclusive flock on the embeddeddolt data
// directory derived from beadsDir. The caller must defer lock.Unlock().
// Returns a no-op lock when serverMode is true (the server handles its own
// concurrency).
func acquireEmbeddedLock(beadsDir string, serverMode bool) (util.Unlocker, error) {
	if serverMode {
		return util.NoopLock{}, nil
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	lock, err := util.TryLock(filepath.Join(dataDir, ".lock"))
	if err != nil {
		if lockfile.IsLocked(err) {
			return nil, fmt.Errorf("embeddeddolt: another process holds the exclusive lock on %s; "+
				"the embedded backend supports only one writer at a time — "+
				"use the dolt server backend for concurrent access", dataDir)
		}
		return nil, fmt.Errorf("embeddeddolt: acquiring lock: %w", err)
	}
	return lock, nil
}

// newDoltStoreFromConfig creates a storage backend from the beads directory's
// persisted metadata.json configuration. Uses embedded Dolt by default;
// connects to dolt sql-server when dolt_mode is "server".
//
// For embedded mode, legacy hyphenated database names (pre-GH#2142) are
// auto-sanitized to underscores and the fix is persisted to metadata.json.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		// A present-but-unloadable metadata.json must not degrade to the
		// embedded default: on server-mode deployments the embedded
		// directory is an empty relic, and opening it silently turns every
		// query into an empty result set with exit 0 (false-empty). Absent
		// metadata.json (cfg == nil, err == nil) keeps the embedded default.
		return nil, fmt.Errorf("load %s: %w (refusing to fall back to the embedded store)", configfile.ConfigPath(beadsDir), err)
	}
	if err := validateConfiguredBackend(cfg); err != nil {
		return nil, err
	}
	if cfg != nil && cfg.IsDoltProxiedServerMode() {
		// TODO: this needs to be uow provider
		return nil, fmt.Errorf("proxy server store should be uow provider")
		// 	return newProxiedServerStore(ctx, &dolt.Config{
		// 		BeadsDir:      beadsDir,
		// 		Database:      cfg.GetDoltDatabase(),
		// 		ProxiedServer: true,
		// 	})
	}
	if cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfig(ctx, beadsDir)
	}
	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	if sanitized := sanitizeDBName(database); sanitized != database {
		if err := migrateHyphenatedDB(beadsDir, cfg, database, sanitized); err != nil {
			return nil, fmt.Errorf("auto-sanitize database name %q → %q: %w", database, sanitized, err)
		}
		database = sanitized
	}
	return embeddeddolt.Open(ctx, beadsDir, database, "main")
}

// migrateHyphenatedDB renames a legacy hyphenated database directory and
// persists the sanitized name to metadata.json so subsequent opens use it.
// This handles projects initialized before GH#2142 that upgrade to
// embedded-mode-default builds (GH#3231).
func migrateHyphenatedDB(beadsDir string, cfg *configfile.Config, oldName, newName string) error {
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	oldDir := filepath.Join(dataDir, oldName)
	newDir := filepath.Join(dataDir, newName)

	oldExists := false
	if info, err := os.Stat(oldDir); err == nil && info.IsDir() {
		oldExists = true
	}

	if oldExists {
		_, newErr := os.Stat(newDir)
		switch {
		case newErr == nil:
			return fmt.Errorf("cannot auto-migrate database: both %q and %q exist under %s; remove one manually and retry",
				oldName, newName, dataDir)
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

// newReadOnlyStoreFromConfig creates a read-only storage backend from the beads
// directory's persisted metadata.json configuration.
//
// For embedded mode, invalid characters (hyphens, dots) are sanitized in-memory
// only — no directory renames or metadata.json writes. This prevents cross-repo
// hydration from mutating foreign projects (GH#3231).
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		// Same contract as newDoltStoreFromConfig: a present-but-unloadable
		// metadata.json is a hard error, not a silent embedded fallback —
		// and the error must name the real cause rather than the downstream
		// "database not found" the embedded open would produce.
		return nil, fmt.Errorf("load %s: %w (refusing to fall back to the embedded store)", configfile.ConfigPath(beadsDir), err)
	}
	if err := validateConfiguredBackend(cfg); err != nil {
		return nil, err
	}
	if cfg != nil && cfg.IsDoltProxiedServerMode() {
		// TODO: this needs to be uow provider
		return nil, fmt.Errorf("proxy server store needs to be uow provider")
		// return newProxiedServerStore(ctx, &dolt.Config{
		// 	BeadsDir:      beadsDir,
		// 	Database:      cfg.GetDoltDatabase(),
		// 	ProxiedServer: true,
		// 	ReadOnly:      true,
		// })
	}
	if cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	}
	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	if sanitized := sanitizeDBName(database); sanitized != database {
		database = sanitized
	}
	// OpenReadOnly, not Open: a read-only open of a foreign project must not
	// run the remote-migrate gate (a behind, remote-backed database would fail
	// hard) and must not write migrations into the target's history
	// (bd-6dnrw.32, GH#3231).
	return embeddeddolt.OpenReadOnly(ctx, beadsDir, database, "main")
}
