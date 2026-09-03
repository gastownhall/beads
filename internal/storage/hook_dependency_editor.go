package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// DependencyEditor returns the inner store's dependency-edge surface with this
// decorator's completion hooks layered over it.
//
// It recurses instead of delegating. A dependency change is an update to the
// issue the edge leaves, so the legacy AddDependency and RemoveDependency
// paths fire the update hook with a dependency-hydrated snapshot; a blind
// delegation would silently stop firing it the moment a front door moved onto
// the role.
func (h *HookFiringStore) DependencyEditor() (issueops.DependencyEditor, error) {
	inner, err := h.inner.DependencyEditor()
	if err != nil {
		return nil, err
	}
	return &hookDependencyEditor{inner: inner, hooks: h}, nil
}

type hookDependencyEditor struct {
	inner issueops.DependencyEditor
	hooks issueOperationHooks
}

// AddDependencies fires the update hook once per DISTINCT source issue, in the
// order those issues first appear in the request. Per source rather than per
// edge because a hook script is written against one issue: two edges leaving
// the same issue are one change to it, and firing twice would have a script
// react to a graph it already saw.
func (e *hookDependencyEditor) AddDependencies(ctx context.Context, request issueops.AddDependenciesRequest) (issueops.AddDependenciesResult, error) {
	result, err := e.inner.AddDependencies(ctx, request)
	if err != nil {
		return result, err
	}
	seen := make(map[string]struct{}, len(result.Added))
	for _, edge := range result.Added {
		if _, ok := seen[edge.IssueID]; ok {
			continue
		}
		seen[edge.IssueID] = struct{}{}
		e.hooks.CompleteIssueOperationDependency(ctx, edge.IssueID)
	}
	return result, nil
}

// RemoveDependency fires the update hook only for an edge that was actually
// there. A no-op removal changed no graph, and a hook fired for it is a hook a
// replayed teardown runs on every pass.
func (e *hookDependencyEditor) RemoveDependency(ctx context.Context, request issueops.RemoveDependencyRequest) (issueops.RemoveDependencyResult, error) {
	result, err := e.inner.RemoveDependency(ctx, request)
	if err == nil && result.Removed {
		e.hooks.CompleteIssueOperationDependency(ctx, request.IssueID)
	}
	return result, err
}
