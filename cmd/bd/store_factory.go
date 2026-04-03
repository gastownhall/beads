//go:build cgo

package main

import (
	"context"
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
func newDoltStore(ctx context.Context, cfg *dolt.Config, opts ...embeddeddolt.Option) (storage.DoltStorage, error) {
	if cfg.ServerMode {
		return dolt.New(ctx, cfg)
	}
	return embeddeddolt.New(ctx, cfg.BeadsDir, cfg.Database, "main", opts...)
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
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfig(ctx, beadsDir)
	}
	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	return embeddeddolt.New(ctx, beadsDir, database, "main")
}

// newReadOnlyStoreFromConfig creates a read-only storage backend from the beads
// directory's persisted metadata.json configuration.
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.IsDoltServerMode() {
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	}
	// Embedded dolt is single-process so read-only is not enforced at the engine level.
	return newDoltStoreFromConfig(ctx, beadsDir)
}
