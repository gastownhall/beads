//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/backend"
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

// newDoltStoreFromConfig creates the storage backend selected by the beads
// directory's persisted metadata.json configuration. The legacy function name
// is retained for command call sites that predate configurable backends.
//
// For embedded mode, legacy hyphenated database names (pre-GH#2142) are
// auto-sanitized to underscores and the fix is persisted to metadata.json.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	store, _, err := backend.OpenConfigured(ctx, beadsDir, backend.ConfiguredOpenOptions{})
	return store, err
}

// migrateHyphenatedDB renames a legacy hyphenated database directory and
// persists the sanitized name to metadata.json so subsequent opens use it.
// This handles projects initialized before GH#2142 that upgrade to
// embedded-mode-default builds (GH#3231).
func migrateHyphenatedDB(beadsDir string, cfg *configfile.Config, oldName, newName string) error {
	return backend.MigrateHyphenatedDatabase(beadsDir, cfg, oldName, newName)
}

// newReadOnlyStoreFromConfig creates a read-only storage backend from the beads
// directory's persisted metadata.json configuration.
//
// For embedded mode, invalid characters (hyphens, dots) are sanitized in-memory
// only — no directory renames or metadata.json writes. This prevents cross-repo
// hydration from mutating foreign projects (GH#3231).
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	store, _, err := backend.OpenConfigured(ctx, beadsDir, backend.ConfiguredOpenOptions{ReadOnly: true})
	return store, err
}
