//go:build cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// OpenBestAvailable opens a beads database using the best available backend
// for the given .beads directory. It reads metadata.json to determine the
// configured mode:
//
//   - Embedded Dolt (default): Opens via the CGo embedded Dolt engine.
//   - Dolt server: Connects to a dolt sql-server via OpenFromConfig.
//
// The returned Storage must be closed when no longer needed.
//
// beadsDir is the path to the .beads directory.
//
// OpenBestAvailable is OpenBestAvailableWith with a zero OpenOptions; see that
// function for per-open injections into a registered backend.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	return OpenBestAvailableWith(ctx, beadsDir, OpenOptions{})
}

// OpenBestAvailableWith is OpenBestAvailable plus per-open injections
// (Credential, HTTPClient, UserAgent) for a registered backend that
// implements Backend.OpenWith. It exists for an embedder whose single
// process serves many workspaces with distinct credentials (gc, Gas City is
// the motivating case) against a backend whose dialer would otherwise be
// process-global, making per-workspace credentials impossible through plain
// OpenBestAvailable.
//
// A zero OpenOptions behaves IDENTICALLY to OpenBestAvailable for every
// backend, registered or not: Dolt and embedded Dolt have no OpenWith and
// never see opts; a registered backend without OpenWith falls back to its
// Open exactly as OpenBestAvailable always has. The one exception across
// every path is a non-nil opts.Credential, non-nil opts.HTTPClient, or
// non-empty opts.UserAgent with nothing able to honor it — that is always a
// typed refusal (the matching ErrXWithoutOpenWith sentinel), never a silent
// open with the field ignored.
func OpenBestAvailableWith(ctx context.Context, beadsDir string, opts OpenOptions) (Storage, error) {
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
	// instead of a silently-opened embedded Dolt store. OpenWithOptions calls
	// OpenWith when the backend has one, else falls back to Open — except a
	// non-nil Credential, non-nil HTTPClient, or non-empty UserAgent, which it
	// refuses rather than silently drops.
	if backend, ok := backends.Lookup(cfg.GetBackend()); ok {
		return backend.OpenWithOptions(ctx, beadsDir, opts)
	}

	// No registered backend claims this workspace: it is plain Dolt
	// (embedded or server), which has no per-open seam at all. Silently
	// dropping a caller-supplied Credential, HTTPClient, or UserAgent here
	// would be the same silent-loss hazard OpenWithOptions refuses for a
	// registered backend without OpenWith (L2: fail closed on all three,
	// not just Credential).
	if opts.Credential != nil {
		return nil, fmt.Errorf("beads: OpenOptions.Credential is not supported for backend %q (Dolt has no per-open credential seam): %w", cfg.GetBackend(), backends.ErrCredentialWithoutOpenWith)
	}
	if opts.HTTPClient != nil {
		return nil, fmt.Errorf("beads: OpenOptions.HTTPClient is not supported for backend %q (Dolt has no per-open transport seam): %w", cfg.GetBackend(), backends.ErrHTTPClientWithoutOpenWith)
	}
	if opts.UserAgent != "" {
		return nil, fmt.Errorf("beads: OpenOptions.UserAgent is not supported for backend %q (Dolt has no per-open transport seam): %w", cfg.GetBackend(), backends.ErrUserAgentWithoutOpenWith)
	}

	if cfg.IsDoltServerMode() {
		store, err := dolt.NewFromConfig(ctx, beadsDir)
		if err != nil {
			return nil, err
		}
		return store, nil
	}

	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		return nil, err
	}
	return store, nil
}
