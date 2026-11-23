package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// LeaseReclaimer returns the inner store's lease-sweep surface with this
// decorator's completion hooks layered over it.
//
// It WRAPS rather than recursing unwrapped, for the reason Releaser does: a
// reclaim changes an issue's assignee and status per reverted row, which is
// an on_update in the vocabulary internal/hooks publishes, and the row it
// names is still there to hand a script.
//
// It recurses instead of delegating, so a blind delegation would hand back
// the inner store's surface and silently stop firing it.
func (h *HookFiringStore) LeaseReclaimer() (issueops.LeaseReclaimer, error) {
	inner, err := h.inner.LeaseReclaimer()
	if err != nil {
		return nil, err
	}
	return &hookLeaseReclaimer{inner: inner, hooks: h}, nil
}

type hookLeaseReclaimer struct {
	inner issueops.LeaseReclaimer
	hooks issueOperationHooks
}

// Reclaim fires the update hook once for every row the sweep reverted.
func (r *hookLeaseReclaimer) Reclaim(ctx context.Context, request issueops.ReclaimRequest) (issueops.ReclaimResult, error) {
	result, err := r.inner.Reclaim(ctx, request)
	if err != nil {
		return result, err
	}
	for _, reclaimed := range result.Reclaimed {
		r.hooks.CompleteIssueOperationReclaim(ctx, reclaimed.ID)
	}
	return result, nil
}
