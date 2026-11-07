package main

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// openForeignProxiedExternalRead serves openForeignStore's foreignRead arm
// for a proxied-server target whose sidecar names an external SQL server
// (#5861 item 2): the only supported way a foreign read reaches a
// proxied-server workspace. Every other target — and every other access —
// keeps the factory dispatch below openForeignStore, whose generic proxied
// refusal (errProxiedStoreUnrouted, in both build-tag factories) this helper
// deliberately does not touch.
//
// It mirrors openNonMutatingStoreFromConfig's order — discovery refusal, a
// non-migrating metadata load that fails on corrupt metadata, backend
// validation, registered-backend precedence — and then, for the built-in
// Dolt backend in proxied-server mode only, decides on the sidecar:
//
//   - absent sidecar, or a sidecar with no External block (a managed-local
//     proxied workspace): the typed proxy.store.unrouted refusal, as before.
//   - an unreadable or malformed sidecar: an error naming its path. The
//     target's configuration is unconfirmable, not absent.
//   - an External block: validate it, connect read-only to the endpoint it
//     names, using the target's literal persisted dolt_database (never the
//     environment-sensitive getter) and the external password from
//     BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD, where empty is authoritative.
//
// handled reports whether beadsDir was this helper's to answer (store or
// error); false means beadsDir is not a built-in Dolt proxied-server target
// and the caller proceeds with the factory dispatch unchanged. The two
// pre-classification refusals — discovery and a metadata load failure —
// return false with their error: both are generic to any dispatch (the
// factories answer an unloadable metadata.json with the same text), so the
// caller returns them either way.
func openForeignProxiedExternalRead(ctx context.Context, beadsDir string) (store storage.DoltStorage, handled bool, err error) {
	if _, err := checkDiscoveredBackend(beadsDir); err != nil {
		return nil, false, err
	}
	// A foreign read never migrates a legacy config.json (LoadForDiscovery),
	// and a present-but-unloadable metadata.json is a hard error naming the
	// real cause — the same contract as the factories.
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		return nil, false, fmt.Errorf("load %s: %w (refusing to fall back to the embedded store)",
			remedy.PrintedValue(configfile.MetadataFilePath(beadsDir)), remedy.PrintedError(err))
	}
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return nil, false, err
	}
	cfg = normalizeLoadedConfig(cfg)
	// Not this helper's target: a registered backend's workspace is never in
	// proxied-server mode (IsDoltProxiedServerMode answers false for any
	// backend but the built-in Dolt one, so the factory's
	// registered-backend-precedent dispatch keeps those opens), and any other
	// workspace that is not proxied-server mode is the factory's too.
	if !cfg.IsDoltProxiedServerMode() {
		return nil, false, nil
	}

	sidecarPath := remedy.PrintedValue(configfile.ProxiedServerClientInfoPath(beadsDir))
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return nil, true, fmt.Errorf("reading %s: %w", sidecarPath, remedy.PrintedError(err))
	}
	if info == nil || info.External == nil {
		// No sidecar, or a managed-local proxied workspace: the proxy (and the
		// database behind it) is owned by bd's own lifecycle, and a foreign
		// read has no way into it. The typed refusal, not a plain error: the
		// dep-add warning suppression (depTargetLookupMiss) keys on it.
		return nil, true, errProxiedStoreUnrouted()
	}
	if err := info.External.Validate(); err != nil {
		return nil, true, fmt.Errorf("%s names an invalid external server: %w", sidecarPath, err)
	}
	// The literal persisted field, not GetDoltDatabase(): the environment-
	// sensitive getter would let the launcher's BEADS_DOLT_SERVER_DATABASE
	// answer for a foreign workspace, and this open must not mutate process
	// environment to point any other path at the target either.
	database := cfg.DoltDatabase
	if database == "" {
		return nil, true, fmt.Errorf("%s names no dolt_database for the proxied-server target at %s",
			remedy.PrintedValue(configfile.MetadataFilePath(beadsDir)), remedy.PrintedValue(beadsDir))
	}
	password := os.Getenv(configfile.ExternalDoltPasswordEnvVar)
	s, err := dolt.NewExternalReadOnly(ctx, beadsDir, database, *info.External, password)
	if err != nil {
		return nil, true, err
	}
	return s, true, nil
}
