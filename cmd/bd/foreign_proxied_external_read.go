package main

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// openExternalReadStore is newExternalReadStore, a var so a unit test can see
// what a foreign read of a proxied-server workspace dials without a server.
var openExternalReadStore = newExternalReadStore

// newExternalReadStore opens rc read-only (dolt.NewExternalReadOnly). A
// failed open returns a nil interface, not one holding a nil *DoltStore.
func newExternalReadStore(ctx context.Context, rc dolt.ExternalReadConfig) (storage.DoltStorage, error) {
	store, err := dolt.NewExternalReadOnly(ctx, rc)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// openForeignProxiedExternalRead is openForeignStore's foreignRead arm for a
// proxied-server workspace (gastownhall/beads#5861). handled is false for
// every other workspace, which keeps its metadata store factory dispatch
// (newReadOnlyStoreFromConfig), a registered backend's included.
//
// A proxied-server workspace has no direct store: its proxy owns the
// connection, and the factories refuse it (errProxiedStoreUnrouted) rather
// than open its root behind the proxy's back. One whose sidecar names an
// external Dolt server is the exception for a read: that server is reachable
// without the proxy, so the read connects to it directly, as the sidecar's
// user, to the workspace's own dolt_database, through the strict
// non-mutating open dolt.NewExternalReadOnly makes. The password is
// configfile.ExternalDoltPasswordEnvVar's, the one the workspace's own proxy
// is given, empty included. A workspace whose server bd manages (no
// external endpoint in its sidecar, or no sidecar) keeps the refusal: its
// server is reachable only through its proxy.
func openForeignProxiedExternalRead(ctx context.Context, beadsDir string) (storage.DoltStorage, bool, error) {
	// Read in place, never migrated, as the factory's non-mutating open does.
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		return nil, true, fmt.Errorf("load %s: %w", remedy.PrintedValue(configfile.MetadataFilePath(beadsDir)), remedy.PrintedError(err))
	}
	if cfg == nil || !cfg.IsDoltProxiedServerMode() {
		return nil, false, nil
	}
	if backends.Registered(cfg.GetBackend()) {
		return nil, false, nil
	}
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return nil, true, err
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return nil, true, fmt.Errorf("load %s: %w", remedy.PrintedValue(configfile.ProxiedServerClientInfoPath(beadsDir)), remedy.PrintedError(err))
	}
	if info == nil || info.External == nil {
		return nil, true, errProxiedStoreUnrouted()
	}
	if err := info.External.Validate(); err != nil {
		return nil, true, fmt.Errorf("%s: %w", remedy.PrintedValue(configfile.ProxiedServerClientInfoPath(beadsDir)), err)
	}
	// The literal dolt_database, not GetDoltDatabase: the launcher's
	// BEADS_DOLT_SERVER_DATABASE must not name another workspace's database.
	if cfg.DoltDatabase == "" {
		return nil, true, fmt.Errorf("%s names no dolt_database for its external server", remedy.PrintedValue(configfile.MetadataFilePath(beadsDir)))
	}
	store, err := openExternalReadStore(ctx, dolt.ExternalReadConfig{
		BeadsDir: beadsDir,
		Database: cfg.DoltDatabase,
		External: *info.External,
		Password: os.Getenv(configfile.ExternalDoltPasswordEnvVar),
	})
	return store, true, err
}
