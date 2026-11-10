package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// IssueClaimer returns the inner store's claimer with this decorator's
// completion hooks layered over it.
//
// It recurses for the same reason IssueLifecycle does: a claim is a write, and
// a blind delegation would hand back the inner store's claimer unchanged and
// silently drop the hook every landed claim owes.
func (h *HookFiringStore) IssueClaimer() (issueops.Claimer, error) {
	inner, err := h.inner.IssueClaimer()
	if err != nil {
		return nil, err
	}
	return &hookIssueClaimer{inner: inner, hooks: h}, nil
}

type hookIssueClaimer struct {
	inner issueops.Claimer
	hooks issueOperationHooks
}

// Claim fires the update hook for a claim that persisted a mutation, and
// suppresses it for the idempotent re-claim — the same no-op suppression
// Reopen applies. Without it an agent polling its own claim would run the
// user's hook script once per poll for a write that never happened.
func (c *hookIssueClaimer) Claim(ctx context.Context, request issueops.ClaimRequest) (issueops.ClaimResult, error) {
	result, err := c.inner.Claim(ctx, request)
	if err == nil && result.Changed {
		c.hooks.CompleteIssueOperationUpdate(result.Issue)
	}
	return result, err
}
