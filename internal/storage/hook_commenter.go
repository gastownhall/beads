package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Commenter returns the inner store's add-comment surface with this
// decorator's completion hooks layered over it.
//
// It recurses instead of delegating, for the reason IssueLifecycle does: the
// legacy AddIssueComment path fires the update hook, and a blind delegation
// would hand back the inner store's commenter and silently stop firing it.
func (h *HookFiringStore) Commenter() (issueops.Commenter, error) {
	inner, err := h.inner.Commenter()
	if err != nil {
		return nil, err
	}
	return &hookCommenter{inner: inner, hooks: h}, nil
}

type hookCommenter struct {
	inner issueops.Commenter
	hooks issueOperationHooks
}

// AddComment fires the update hook for the commented issue, which is what the
// legacy comment path fires: a comment is a change to the issue as far as a
// hook script is concerned, and there is no on_comment event to fire instead.
func (c *hookCommenter) AddComment(ctx context.Context, request issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	result, err := c.inner.AddComment(ctx, request)
	if err == nil && result.Comment != nil {
		c.hooks.CompleteIssueOperationComment(ctx, result.Comment.IssueID)
	}
	return result, err
}
