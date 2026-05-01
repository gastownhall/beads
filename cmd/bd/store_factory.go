//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// isEmbeddedMode returns true when the current session is using the embedded
// Dolt engine (the default). Returns false in server mode (external dolt
// sql-server). Safe to call before store initialization — defaults to true
// (embedded) when the mode hasn't been set yet.
func isEmbeddedMode() bool {
	if shouldUseGlobals() {
		if serverMode {
			return false
		}
	} else if cmdCtx != nil && cmdCtx.ServerMode {
		return false
	}
	// Shared server mode is a form of server mode. This check covers
	// commands that skip DB init (dolt status, dolt start, etc.) where
	// serverMode hasn't been set from metadata.json yet (GH#2946).
	if doltserver.IsSharedServerMode() {
		return false
	}
	return true // default: embedded
}

// newDoltStore creates a storage backend from an explicit config.
// When cfg.ServerMode is true, connects to an external dolt sql-server;
// otherwise uses the embedded Dolt engine (default).
// Used by bd init and PersistentPreRun.
func newDoltStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
	if cfg.ServerMode {
		return dolt.New(ctx, cfg)
	}
	return embeddeddolt.Open(ctx, cfg.BeadsDir, cfg.Database, "main")
}

// acquireEmbeddedLock acquires an exclusive flock on the embeddeddolt data
// directory derived from beadsDir. The caller must defer lock.Unlock().
// Returns a no-op lock when serverMode is true (the server handles its own
// concurrency).
func acquireEmbeddedLock(beadsDir string, serverMode bool) (embeddeddolt.Unlocker, error) {
	if serverMode {
		return embeddeddolt.NoopLock{}, nil
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	return embeddeddolt.TryLock(dataDir)
}

// newDoltStoreFromConfig creates a storage backend from the beads directory's
// persisted metadata.json configuration. Uses embedded Dolt by default;
// connects to dolt sql-server when dolt_mode is "server".
//
// For embedded mode, legacy hyphenated database names (pre-GH#2142) are
// auto-sanitized to underscores and the fix is persisted to metadata.json.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
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
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	}
	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	if sanitized := sanitizeDBName(database); sanitized != database {
		database = sanitized
	}
	return embeddeddolt.Open(ctx, beadsDir, database, "main")
}
