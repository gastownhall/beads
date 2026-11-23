//go:build !cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// OpenBestAvailable opens a beads database using the best available backend
// for the given .beads directory. In non-CGO builds, Dolt server mode and
// proxied-server mode (through the workspace's bd proxy, exactly as in the cgo
// build) are supported; embedded Dolt returns an error directing the user to
// server mode. As in the cgo build, a proxied-server workspace takes the
// server arm only when the environment forces server mode
// (BEADS_DOLT_SERVER_MODE or BEADS_DOLT_SHARED_SERVER) and names the port
// (BEADS_DOLT_SERVER_PORT).
//
// beadsDir is the path to the .beads directory.
//
// OpenBestAvailable is OpenBestAvailableWith with a zero OpenOptions; see that
// function for per-open injections into a registered backend. Mirrors the
// cgo build exactly for every path: a registered backend dispatches through
// OpenWithOptions regardless of CGO, so this build's only Dolt-specific
// difference from beads_cgo.go is the embedded-Dolt arm.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	return OpenBestAvailableWith(ctx, beadsDir, OpenOptions{})
}

// OpenBestAvailableWith is OpenBestAvailable plus per-open injections
// (Credential, HTTPClient, UserAgent) for a registered backend that
// implements Backend.OpenWith. See the cgo build's doc comment for the full
// rationale; behavior here is identical except that embedded Dolt is
// unavailable without CGO.
func OpenBestAvailableWith(ctx context.Context, beadsDir string, opts OpenOptions) (Storage, error) {
	// Before Load, which can migrate a legacy config.json, and before any
	// backend is chosen; IsSupportedBackend below re-checks the loaded config.
	if err := checkDiscoveredBackend(beadsDir); err != nil {
		return nil, err
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading storage metadata: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if !configfile.IsSupportedBackend(cfg.Backend) {
		return nil, configuredBackendUnavailable(cfg.Backend, beadsDir, cfg)
	}

	// Dispatch to a registered extension backend before any Dolt path, mirroring
	// the CLI store factories so SDK callers get the backend they registered
	// instead of the embedded-Dolt-requires-CGO error. OpenWithOptions calls
	// OpenWith when the backend has one, else falls back to Open — except a
	// non-nil Credential, non-nil HTTPClient, or non-empty UserAgent, which it
	// refuses rather than silently drops.
	if backend, ok := backends.Lookup(cfg.GetBackend()); ok {
		return backend.OpenWithOptions(ctx, beadsDir, opts)
	}

	// No registered backend claims this workspace: it is plain Dolt (server
	// mode is the only option without CGO), which has no per-open seam at
	// all. Silently dropping a caller-supplied Credential, HTTPClient, or
	// UserAgent here would be the same silent-loss hazard OpenWithOptions
	// refuses for a registered backend without OpenWith (L2: fail closed on
	// all three, not just Credential).
	if opts.Credential != nil {
		return nil, fmt.Errorf("beads: OpenOptions.Credential is not supported for backend %q (Dolt has no per-open credential seam): %w", cfg.GetBackend(), backends.ErrCredentialWithoutOpenWith)
	}
	if opts.HTTPClient != nil {
		return nil, fmt.Errorf("beads: OpenOptions.HTTPClient is not supported for backend %q (Dolt has no per-open transport seam): %w", cfg.GetBackend(), backends.ErrHTTPClientWithoutOpenWith)
	}
	if opts.UserAgent != "" {
		return nil, fmt.Errorf("beads: OpenOptions.UserAgent is not supported for backend %q (Dolt has no per-open transport seam): %w", cfg.GetBackend(), backends.ErrUserAgentWithoutOpenWith)
	}

	if routesThroughProxy(cfg) {
		// Before the server arm, which BEADS_DOLT_SERVER_MODE or
		// BEADS_DOLT_SHARED_SERVER would otherwise select ahead of dolt_mode
		// (unless BEADS_DOLT_SERVER_PORT names the port to dial, which
		// keeps that arm: see routesThroughProxy), and before the
		// embedded-Dolt-requires-CGO error: a proxied workspace is served
		// through its bd proxy, as on the bd CLI (GH#8323).
		return openProxiedServer(ctx, beadsDir)
	}

	if cfg.IsDoltServerMode() {
		store, err := dolt.NewFromConfig(ctx, beadsDir)
		if err != nil {
			return nil, err
		}
		return store, nil
	}
	return nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
}
