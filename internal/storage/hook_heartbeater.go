package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Heartbeater returns the inner store's lease-refresh surface with this
// decorator's completion hook layered over it.
//
// A REFRESH FIRES NOTHING. It writes only the ephemeral lease row: it changes
// no field a hook script could read back, it is the hottest write in a fleet,
// and the raw HeartbeatIssue has never fired a hook. Firing on_update for it
// would run a user's subprocess every ninety seconds per worker for a row
// that did not change.
//
// A RE-ARM FIRES on_update, once. It is recorded as a mutation (row_lock,
// updated_at, an event, a journal entry, a version), the row it names is
// still there to hand a script, and the result's Rearmed is the fact that the
// row was written — the Releaser's Changed rule.
//
// It recurses instead of delegating, so a blind delegation would hand back
// the inner store's surface and silently stop firing it.
func (h *HookFiringStore) Heartbeater() (issueops.Heartbeater, error) {
	inner, err := h.inner.Heartbeater()
	if err != nil {
		return nil, err
	}
	return &hookHeartbeater{inner: inner, hooks: h}, nil
}

type hookHeartbeater struct {
	inner issueops.Heartbeater
	hooks issueOperationHooks
}

// Heartbeat fires the update hook for a re-arm and nothing for a refresh or
// a refusal.
func (b *hookHeartbeater) Heartbeat(ctx context.Context, request issueops.HeartbeatRequest) (issueops.HeartbeatResult, error) {
	result, err := b.inner.Heartbeat(ctx, request)
	if err != nil || !result.Rearmed {
		return result, err
	}
	b.hooks.CompleteIssueOperationUpdateByID(ctx, result.IssueID)
	return result, nil
}
