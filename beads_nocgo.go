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
// for the given .beads directory. In non-CGO builds, only Dolt server mode is
// supported; embedded Dolt returns an error directing the user to server mode.
// A Dolt proxied-server workspace is refused with ErrProxiedServerWorkspace,
// exactly as in the cgo build: its data is served by its proxy, which no arm
// here can open, and the workspace is left untouched.
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
// unavailable without CGO. A Dolt proxied-server workspace is refused with
// ErrProxiedServerWorkspace after the per-open option refusals, exactly as
// in the cgo build.
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

	// A proxied-server workspace is served by its proxy's sql-server, which
	// neither arm below can reach: this build has no embedded arm, and the
	// server arm would bypass the proxy (GH#6655). Refuse before either
	// constructor, including under a server-forcing environment override,
	// which IsDoltServerMode consults ahead of the persisted mode.
	if cfg.IsDoltProxiedServerMode() {
		return nil, proxiedServerWorkspaceError(beadsDir)
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
