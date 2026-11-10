package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Releaser returns the inner store's claim-release surface with this
// decorator's completion hooks layered over it.
//
// It WRAPS rather than recursing unwrapped, unlike Sweeper and Deleter: a
// release changes an issue's assignee and status, which is an on_update in the
// vocabulary internal/hooks publishes, and the row it names is still there to
// hand a script. The journal already classifies a release the same way — the
// shared body records EventUpdate for it — so wrapping here is agreement rather
// than a new opinion.
//
// It recurses instead of delegating, for the reason Commenter does: the write
// this role replaces — `bd unclaim` through the store's own UnclaimIssue —
// reaches the hook layer today, so a blind delegation would hand back the inner
// store's surface and silently stop firing it.
func (h *HookFiringStore) Releaser() (issueops.Releaser, error) {
	inner, err := h.inner.Releaser()
	if err != nil {
		return nil, err
	}
	return &hookReleaser{inner: inner, hooks: h}, nil
}

type hookReleaser struct {
	inner issueops.Releaser
	hooks issueOperationHooks
}

// Release fires the update hook for a release that landed, and fires nothing
// for a refusal.
//
// RESULT.CHANGED IS THE CONDITION rather than "err == nil", even though the two
// agree on every answer the role returns today. The role documents Changed as
// the fact "the row was written", and reading the fact is what keeps this
// decorator correct if the role ever answers a release that wrote nothing —
// where firing on_update would report a change no reader of the row could see.
// Inferring it from the absence of an error would make this file quietly wrong
// on that day, in a direction no test names.
func (r *hookReleaser) Release(ctx context.Context, request issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	result, err := r.inner.Release(ctx, request)
	if err != nil || !result.Changed {
		return result, err
	}
	r.hooks.CompleteIssueOperationRelease(ctx, request.IssueID)
	return result, nil
}
