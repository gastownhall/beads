package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BatchCloser returns the inner store's batch-close surface with this
// decorator's completion hooks layered over it.
//
// It recurses instead of delegating, for the reason IssueLifecycle does: a
// blind delegation would hand back the inner store's closer and silently drop
// every on_close script the decorator exists to run.
func (h *HookFiringStore) BatchCloser() (issueops.BatchCloser, error) {
	inner, err := h.inner.BatchCloser()
	if err != nil {
		return nil, err
	}
	return &hookBatchCloser{inner: inner, hooks: h}, nil
}

type hookBatchCloser struct {
	inner issueops.BatchCloser
	hooks issueOperationHooks
}

// CloseBatch fires the close hook once PER LANDED ITEM, in request order, and
// fires nothing for an item that refused. Per item rather than once for the
// batch because a hook script is written against one issue: collapsing N
// closes into one firing would silently stop reporting N-1 of them.
//
// The claim, when one happened, fires the update hook the claim paths fire,
// after the closes — the same order the transaction applied them in.
func (o *hookBatchCloser) CloseBatch(ctx context.Context, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	result, err := o.inner.CloseBatch(ctx, request)
	if err != nil {
		return result, err
	}
	for _, outcome := range result.Outcomes {
		if outcome.Err == nil {
			o.hooks.CompleteIssueOperationClose(outcome.Issue)
		}
	}
	if result.ClaimedNext != nil {
		o.hooks.CompleteIssueOperationUpdate(result.ClaimedNext.Issue)
	}
	return result, nil
}
