package main

import (
	"context"

	"github.com/steveyegge/beads/internal/debug"
)

// maybeAutoPush pushes to the configured Dolt remote ("origin") after a
// successful commit. This provides automatic backup to a git remote.
//
// Semantics:
//   - No-op in sandbox mode.
//   - No-op if no store is available.
//   - No-op if no remote named "origin" is configured (silent).
//   - No-op if the current command already did an explicit push (bd dolt push).
//   - Errors are logged as warnings, never fatal — the local commit is safe.
func maybeAutoPush(ctx context.Context) {
	if sandboxMode {
		return
	}
	if commandDidExplicitPush {
		return
	}

	st := getStore()
	if st == nil {
		return
	}

	hasRemote, err := st.HasRemote(ctx, "origin")
	if err != nil {
		debug.Logf("auto-push: failed to check remote: %v", err)
		return
	}
	if !hasRemote {
		return
	}

	if err := st.Push(ctx); err != nil {
		debug.Logf("warning: auto-push to remote failed: %v", err)
		return
	}
	debug.Logf("auto-pushed to remote")
}
