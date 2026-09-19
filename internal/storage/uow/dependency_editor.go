package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// DependencyEditorSource is the capability accessor a unit-of-work provider
// offers for the dependency-edge role, the sibling of IssueLifecycleSource and
// BatchCloserSource.
type DependencyEditorSource interface {
	DependencyEditor() (publicops.DependencyEditor, error)
}

// dependencyEditor edits the dependency graph through a unit of work.
type dependencyEditor struct {
	provider UnitOfWorkProvider
}

// DependencyEditor returns the guarded dependency-edge surface for this provider.
func (p *doltSQLProvider) DependencyEditor() (publicops.DependencyEditor, error) {
	return NewDependencyEditor(p)
}

// NewDependencyEditor constructs a public dependency editor backed by provider.
func NewDependencyEditor(provider UnitOfWorkProvider) (publicops.DependencyEditor, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new dependency editor: unit-of-work provider must not be nil")
	}
	return &dependencyEditor{provider: provider}, nil
}

var _ publicops.DependencyEditor = (*dependencyEditor)(nil)

// AddDependencies asserts every edge in ONE unit of work. The request is the
// transaction: N edges are one commit, and the first refusal returns an error
// that rolls every earlier edge back with it, which is what makes the request
// all-or-nothing without any bookkeeping of its own.
//
// The use case applies the parent-child-first ordering and the whole-graph
// final gate the store-backed sibling applies, through the same rules, so the
// two answer the same refusals to the same requests. It is the SOURCE-ROUTED
// bulk verb for the same reason: each edge follows the plane its source lives
// in, and a request may mix them, so the two implementations also land the
// same edges in the same tables.
func (e *dependencyEditor) AddDependencies(ctx context.Context, request publicops.AddDependenciesRequest) (publicops.AddDependenciesResult, error) {
	if err := storageissueops.ValidateAddDependenciesRequest(request); err != nil {
		return publicops.AddDependenciesResult{}, err
	}
	deps := make([]*types.Dependency, 0, len(request.Edges))
	for _, edge := range request.Edges {
		deps = append(deps, &types.Dependency{
			IssueID:     edge.IssueID,
			DependsOnID: edge.DependsOnID,
			Type:        edge.Type,
		})
	}
	added := make([]publicops.DependencyEdge, len(request.Edges))
	copy(added, request.Edges)

	return RunTxResult(ctx, e.provider, func(ctx context.Context, uw UnitOfWork) (publicops.AddDependenciesResult, string, error) {
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, deps, request.Actor, domain.BulkAddDepsOpts{
			SkipPerEdgeCycleCheck: request.SkipPerEdgeCycleCheck,
		}); err != nil {
			return publicops.AddDependenciesResult{}, "", err
		}
		return publicops.AddDependenciesResult{Added: added},
			storageissueops.AddDependenciesCommitMessage(request), nil
	})
}

// RemoveDependency removes one edge in one unit of work.
//
// It is SOURCE-ROUTED, like the add: an edge lives in the plane its source
// lives in, so the removal reads that plane rather than pinning the durable
// one — a pinned removal cannot see a wisp-sourced edge and would report an
// edge that is plainly there as missing (bd-yby99.17).
//
// The delete inside the transaction is the found/not-found verdict. Taking it
// from the write itself rather than from a lookup before it is what keeps the
// answer true: a separate check is a read-then-write window in which a racing
// writer could add or drop the very edge being reported on.
func (e *dependencyEditor) RemoveDependency(ctx context.Context, request publicops.RemoveDependencyRequest) (publicops.RemoveDependencyResult, error) {
	if err := storageissueops.ValidateRemoveDependencyRequest(request); err != nil {
		return publicops.RemoveDependencyResult{}, err
	}
	return RunTxResult(ctx, e.provider, func(ctx context.Context, uw UnitOfWork) (publicops.RemoveDependencyResult, string, error) {
		removed, err := uw.DependencyUseCase().RemoveDependencyBySource(ctx, request.IssueID, request.DependsOnID, request.Actor)
		if err != nil {
			return publicops.RemoveDependencyResult{}, "", err
		}
		if !removed {
			// Nothing to write, so nothing to version: an empty commit message
			// is how RunTxResult is told to commit nothing.
			return publicops.RemoveDependencyResult{Removed: false}, "", nil
		}
		// A wisp-plane removal touched no versioned table, so the commit this
		// asks for finds nothing to commit and RunTxResult absorbs that — the
		// same way the all-ephemeral add records no history entry.
		return publicops.RemoveDependencyResult{Removed: true},
			storageissueops.RemoveDependencyCommitMessage(request.IssueID, request.DependsOnID), nil
	})
}
