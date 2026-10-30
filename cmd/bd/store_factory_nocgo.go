//go:build !cgo

package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func usesSQLServer() bool {
	return true
}

// isEmbeddedMode reports whether the command is using embedded Dolt storage.
func isEmbeddedMode() bool {
	return false
}

// isEmbeddedStore reports whether st is an embedded Dolt store, which a
// non-CGO build never opens.
func isEmbeddedStore(storage.DoltStorage) bool {
	return false
}

func usesProxiedServer() bool {
	if shouldUseGlobals() {
		return proxiedServerMode
	}
	return cmdCtx != nil && cmdCtx.ProxiedServerMode
}

// newRegisteredBackendStore is the non-CGO twin of the CGO build's registry
// factory: identical, since the backend registry does not depend on CGO. It
// exists here too so the root pre-run's registry arm has one activating
// construction path in both builds.
func newRegisteredBackendStore(ctx context.Context, name, beadsDir string, readOnly bool) (s storage.DoltStorage, err error) {
	defer func() { s, err = activateVersionedHistoryStore(ctx, s, err) }()
	defer func() { s, err = activateEventsJournalStore(beadsDir, s, err) }()
	backend, ok := backends.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("storage backend %q is not registered", name)
	}
	if readOnly {
		return backend.OpenReadOnly(ctx, beadsDir)
	}
	return backend.Open(ctx, beadsDir)
}

// newDoltStore applies events-journal activation for the same reason its CGO
// twin does — see the note at the top of events_journal.go.
func newDoltStore(ctx context.Context, cfg *dolt.Config) (s storage.DoltStorage, err error) {
	defer func() { s, err = activateVersionedHistoryStore(ctx, s, err) }()
	defer func() { s, err = activateEventsJournalStore(cfg.BeadsDir, s, err) }()
	if cfg.ProxiedServer {
		return nil, errProxiedStoreUnrouted()
	}
	if !cfg.ServerMode {
		return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
	}
	return dolt.New(ctx, cfg)
}

// acquireEmbeddedLock returns a no-op lock in non-CGO builds.
func acquireEmbeddedLock(_ string, _ bool) (util.Unlocker, error) {
	return util.NoopLock{}, nil
}

// newDoltStoreFromConfig creates a SQL-server-backed storage backend from config.
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (s storage.DoltStorage, err error) {
	defer func() { s, err = activateVersionedHistoryStore(ctx, s, err) }()
	defer func() { s, err = activateEventsJournalStore(beadsDir, s, err) }()
	// Before Load can migrate a legacy config.json; validateConfiguredBackend
	// below re-checks the loaded config.
	discovered, err := checkDiscoveredBackend(beadsDir)
	if err != nil {
		return nil, err
	}
	// This build refuses every embedded open; decide it on the discovered
	// metadata, before Load migrates a legacy config.json, so the refused
	// open writes nothing. Without metadata Load writes nothing either.
	if discovered != nil && selectsEmbeddedStore(beadsDir, discovered) {
		return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		// Name the real cause: without this, a present-but-unloadable
		// metadata.json surfaces as the misleading "embedded requires CGO"
		// message below.
		return nil, fmt.Errorf("load %s: %w", configfile.ConfigPath(beadsDir), err)
	}
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return nil, err
	}
	cfg = normalizeLoadedConfig(cfg)
	if backend, ok := backends.Lookup(cfg.GetBackend()); ok {
		return backend.Open(ctx, beadsDir)
	}
	if cfg != nil && cfg.IsDoltProxiedServerMode() {
		return nil, errProxiedStoreUnrouted()
	}
	if effectiveServerMode(beadsDir, cfg) {
		return dolt.NewFromConfig(ctx, beadsDir)
	}
	return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
}

// newExistingDoltStoreFromConfig is the non-CGO twin of the CGO build's
// routed-write factory. That one differs from newDoltStoreFromConfig only by
// refusing to create a missing EMBEDDED database, and this build has no
// embedded store; a server-mode open never creates a missing database.
func newExistingDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return newDoltStoreFromConfig(ctx, beadsDir)
}

// newReadOnlyStoreFromConfig creates a read-only SQL-server-backed storage
// backend.
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return openNonMutatingStoreFromConfig(ctx, beadsDir, false)
}

// openNonMutatingStoreFromConfig is the body of newReadOnlyStoreFromConfig and
// newPreviewStoreFromConfig; preview selects the latter. It does not activate
// the events journal: the store refuses writes, so there is no mutation for a
// journal row to accompany (exemption is recorded in the construction guard).
// The registry arm is the exception the cgo twin of the same name describes.
func openNonMutatingStoreFromConfig(ctx context.Context, beadsDir string, preview bool) (storage.DoltStorage, error) {
	// Before Load can migrate a legacy config.json; validateConfiguredBackend
	// below re-checks the loaded config.
	discovered, err := checkDiscoveredBackend(beadsDir)
	if err != nil {
		return nil, err
	}
	// This build refuses every embedded open; decide it on the discovered
	// metadata, before Load migrates a legacy config.json, so the refused
	// open writes nothing. Without metadata Load writes nothing either.
	if discovered != nil && selectsEmbeddedStore(beadsDir, discovered) {
		return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
	}
	// Read in place, never migrated: see the cgo twin,
	// openNonMutatingStoreFromConfig.
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", configfile.MetadataFilePath(beadsDir), err)
	}
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return nil, err
	}
	cfg = normalizeLoadedConfig(cfg)
	if backend, ok := backends.Lookup(cfg.GetBackend()); ok {
		return backend.OpenReadOnly(ctx, beadsDir)
	}
	if cfg != nil && cfg.IsDoltProxiedServerMode() {
		return nil, errProxiedStoreUnrouted()
	}
	if effectiveServerMode(beadsDir, cfg) {
		// A read refuses a schema too old for this binary's issue reads at
		// open; a preview tolerates a behind schema, as in the cgo twin.
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true, RequireReadableSchema: !preview})
	}
	return nil, fmt.Errorf("%s", nocgoEmbeddedErrMsg)
}

// newPreviewStoreFromConfig is the non-CGO twin of the CGO build's preview
// factory. This build has no embedded store, so the two differ only in the
// server arm: a preview tolerates a schema behind this binary's.
func newPreviewStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return openNonMutatingStoreFromConfig(ctx, beadsDir, true)
}

const nocgoEmbeddedErrMsg = `embedded Dolt requires a CGO build, but this bd binary was built with CGO_ENABLED=0.

Three options:

  1. Use the proxied dolt sql-server (no external server, no reinstall):
       bd init --proxied-server
     bd spawns a per-workspace proxy + child dolt sql-server under
     .beads/dolt/ and manages their lifecycle for you.

  2. Use external server mode (no reinstall needed):
       bd init --server
     Requires a running 'dolt sql-server'. See docs/architecture/dolt.md.

  3. Reinstall with embedded-mode support:
       brew install beads                              # macOS / Linux
       npm install -g @beads/bd                        # any platform with Node
       curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

See docs/getting-started/installation.md for the full comparison.`
