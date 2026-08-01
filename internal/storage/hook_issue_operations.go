package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// IssueLifecycle returns the inner store's lifecycle with this decorator's
// completion hooks layered over it.
//
// It recurses instead of delegating. A blind delegation would hand back the
// inner store's lifecycle unchanged and silently drop every hook this
// decorator exists to fire — the failure would be invisible until someone
// noticed their on_create script had stopped running.
func (h *HookFiringStore) IssueLifecycle() (issueops.Lifecycle, error) {
	inner, err := h.inner.IssueLifecycle()
	if err != nil {
		return nil, err
	}
	return &hookIssueOperations{inner: inner, hooks: h}, nil
}

// issueOperationHooks is the completion-hook surface hookIssueOperations needs
// from *HookFiringStore. Depending on the interface instead of the concrete
// decorator makes "did this verb fire a hook?" observable at this boundary, so
// the per-verb firing rules below can be tested without a real
// filesystem-backed async hook runner.
type issueOperationHooks interface {
	CompleteIssueOperationCreate(ctx context.Context, issue *types.Issue, dependencies []*types.Dependency)
	CompleteIssueOperationUpdate(issue *types.Issue)
	CompleteIssueOperationClose(issue *types.Issue)
}

var _ issueOperationHooks = (*HookFiringStore)(nil)

type hookIssueOperations struct {
	inner issueops.Lifecycle
	hooks issueOperationHooks
}

// Create fires the create hook, plus an update hook for every issue on the far
// end of an edge the create wrote. The edge list comes from the request
// because a reverse edge never appears in the created issue's own
// dependencies. Reading the request back after the call is safe: an
// issueops.Lifecycle never mutates caller-owned request values.
func (o *hookIssueOperations) Create(ctx context.Context, request issueops.CreateRequest) (issueops.CreateResult, error) {
	result, err := o.inner.Create(ctx, request)
	if err == nil && result.Issue != nil {
		o.hooks.CompleteIssueOperationCreate(ctx, result.Issue, CreatePublicCreateDependencies(result.Issue.ID, request))
	}
	return result, err
}

func (o *hookIssueOperations) Update(ctx context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	result, err := o.inner.Update(ctx, request)
	if err == nil {
		o.hooks.CompleteIssueOperationUpdate(result.Issue)
	}
	return result, err
}

func (o *hookIssueOperations) Close(ctx context.Context, request issueops.CloseRequest) (issueops.CloseResult, error) {
	result, err := o.inner.Close(ctx, request)
	if err == nil {
		o.hooks.CompleteIssueOperationClose(result.Issue)
	}
	return result, err
}

// Reopen suppresses the update hook on a no-op, matching the legacy reopen
// path (hookTrackingLifecycleTransaction.ReopenIssueWithResult). Close and
// Update stay unconditional on success, also matching their legacy paths.
func (o *hookIssueOperations) Reopen(ctx context.Context, request issueops.ReopenRequest) (issueops.ReopenResult, error) {
	result, err := o.inner.Reopen(ctx, request)
	if err == nil && result.Changed {
		o.hooks.CompleteIssueOperationUpdate(result.Issue)
	}
	return result, err
}
