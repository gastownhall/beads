package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BatchCreator returns the inner store's batch-create surface with this
// decorator's completion hooks layered over it.
//
// It recurses instead of delegating, for the reason IssueLifecycle does: a
// blind delegation would hand back the inner store's creator and silently drop
// every on_create script the decorator exists to run.
func (h *HookFiringStore) BatchCreator() (issueops.BatchCreator, error) {
	inner, err := h.inner.BatchCreator()
	if err != nil {
		return nil, err
	}
	return &hookBatchCreator{inner: inner, hooks: h}, nil
}

type hookBatchCreator struct {
	inner issueops.BatchCreator
	hooks issueOperationHooks
}

// CreateBatch fires the create hook once PER ITEM, in request order, and fires
// nothing at all when the batch refused — the batch is all or nothing, so a
// refusal created no issue for a script to be told about. Per item because a
// hook script is written against one issue: collapsing N creates into one
// firing would silently stop reporting N-1 of them.
//
// The edge list per item comes from the REQUEST, exactly as the single create's
// does, because a reverse edge never appears in the created issue's own
// dependencies. Reading the request back after the call is safe: an
// issueops.BatchCreator never mutates caller-owned request values.
func (o *hookBatchCreator) CreateBatch(ctx context.Context, request issueops.CreateBatchRequest) (issueops.CreateBatchResult, error) {
	result, err := o.inner.CreateBatch(ctx, request)
	if err != nil {
		return result, err
	}
	for i, issue := range result.Issues {
		if issue == nil || i >= len(request.Items) {
			continue
		}
		o.hooks.CompleteIssueOperationCreate(ctx, issue, CreatePublicCreateDependencies(issue.ID, issueops.CreateRequest{
			Dependencies: request.Items[i].Dependencies,
		}))
	}
	return result, err
}
