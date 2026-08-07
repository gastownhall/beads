package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/memoryops"
)

// Proxied-server dual of prime's memory read (bd-mm8wf, from lion's #5361
// review). `bd prime` sits in noDbCommands, so the root pre-run opens neither
// a store nor a UOW provider; formatMemoriesForPrime therefore lazily
// initialized storage via ensureStoreActiveForPrime — which in a
// proxied-server workspace meant a DIRECT dolt open against sidecar-owned
// data, exactly the seam class bd-m7zzd closed in relate.go and human.go
// (there in write verbs, here in a read-only limb).
//
// The read rides the proxied plane: memoryops.Memories.List through the
// provider's own capability accessor, on a provider opened scoped to this read.
// It is the same call `bd memories` makes, which is what stopped prime from
// being a fifth front door with its own copy of the kv.memory. prefix rule.
// Prime's contract is preserved on every failure edge: silent skip when the
// plane is unavailable, the timeout banner on a deadline, and rendering through
// the same shared tail as the classic path.

// primeProxiedProviderOpen opens the proxied-plane provider for prime's
// memory read; a var so unit tests can stub the plane. Production opens the
// standard identity-asserting provider, exactly as the root pre-run would for
// a data command in this workspace.
var primeProxiedProviderOpen = func(ctx context.Context, beadsDir string) (uow.UnitOfWorkProvider, error) {
	return newProxiedServerUOWProvider(ctx, beadsDir, "")
}

// formatMemoriesForPrimeProxied is the proxied-mode branch of
// formatMemoriesForPrime. Returns "" whenever the read cannot be served (no
// workspace, provider open failure, read failure): prime must degrade
// silently, never fail the session-start hook.
func formatMemoriesForPrimeProxied(compact bool) string {
	timeout := primeStoreTimeout()
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	plane, err := primeProxiedMemoryPlane(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return formatPrimeMemoryTimeout(compact, timeout)
		}
		return "" // Silently skip — proxied plane unavailable
	}
	return renderPrimeMemoryPlane(plane, compact)
}

// primeProxiedMemoryPlane reads the memory plane through the proxied plane's
// own capability accessor. When the root pre-run opened the global provider (it
// never does for prime itself — noDbCommands — but the branch keeps any future
// caller honest), that provider is used and left open for its owner to close;
// otherwise a provider is opened scoped to this one read and closed before
// returning.
func primeProxiedMemoryPlane(ctx context.Context) (map[string]string, error) {
	provider := uowProvider
	if provider == nil {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return nil, fmt.Errorf("no beads directory found")
		}
		p, err := primeProxiedProviderOpen(ctx, beadsDir)
		if err != nil {
			return nil, err
		}
		defer func() { _ = p.Close(ctx) }()
		provider = p
	}
	memories, err := memoriesFromProvider(provider)
	if err != nil {
		return nil, err
	}
	result, err := memories.List(ctx, memoryops.ListRequest{})
	if err != nil {
		return nil, err
	}
	return result.Memories, nil
}
