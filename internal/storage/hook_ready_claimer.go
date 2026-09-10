package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// ReadyClaimer returns the inner store's claim surface with this decorator's
// completion hooks layered over it.
//
// It recurses instead of delegating. A claim is an update to an issue — it
// sets the assignee and moves the status — so the legacy claim paths fire the
// update hook, and a blind delegation would silently stop firing it the moment
// a front door moved onto the role.
func (h *HookFiringStore) ReadyClaimer() (issueops.ReadyClaimer, error) {
	inner, err := h.inner.ReadyClaimer()
	if err != nil {
		return nil, err
	}
	return &hookReadyClaimer{inner: inner, hooks: h}, nil
}

type hookReadyClaimer struct {
	inner issueops.ReadyClaimer
	hooks issueOperationHooks
}

// ClaimNext fires the update hook for the row it won, and nothing at all for
// an empty front: there is no issue to report, and a hook fired for "nothing
// happened" is a hook a polling agent runs forever.
func (c *hookReadyClaimer) ClaimNext(ctx context.Context, request issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	result, err := c.inner.ClaimNext(ctx, request)
	if err == nil && result.Claimed != nil {
		c.hooks.CompleteIssueOperationUpdate(result.Claimed.Issue)
	}
	return result, err
}
