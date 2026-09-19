package uow

import (
	"context"
	"fmt"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// EdgeReaderSource is the capability accessor a unit-of-work provider offers
// for the stored-edge role, the sibling of RelationsSource and CounterSource.
type EdgeReaderSource interface {
	EdgeReader() (publicops.EdgeReader, error)
}

// edgeReader answers stored-edge reads through a unit of work.
type edgeReader struct {
	provider UnitOfWorkProvider
}

// EdgeReader returns the guarded stored-edge surface for this provider.
func (p *doltSQLProvider) EdgeReader() (publicops.EdgeReader, error) {
	return NewEdgeReader(p)
}

// NewEdgeReader constructs public stored-edge reads backed by provider.
func NewEdgeReader(provider UnitOfWorkProvider) (publicops.EdgeReader, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new edge reader: unit-of-work provider must not be nil")
	}
	return &edgeReader{provider: provider}, nil
}

var _ publicops.EdgeReader = (*edgeReader)(nil)

// ReadEdges answers one stored-edge read inside ONE unit of work, so the
// anchor probe and the edge read see one snapshot — the property the two
// store-backed bodies get from a shared read transaction.
//
// The probe is the two batched by-id reads, one per plane, rather than the
// per-id issue-then-wisp fallback Relations uses: this role's question is asked
// about many anchors at once, and a probe that fanned out per id would put back
// the round trips the batched edge read removed.
func (r *edgeReader) ReadEdges(ctx context.Context, request publicops.EdgeReadRequest) (publicops.EdgeReadResult, error) {
	if err := storageissueops.ValidateEdgeReadRequest(request); err != nil {
		return publicops.EdgeReadResult{}, err
	}
	anchors := storageissueops.EdgeReadAnchors(request.IDs)
	if len(anchors) == 0 {
		return publicops.EdgeReadResult{Anchors: []publicops.AnchorEdges{}}, nil
	}
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (publicops.EdgeReadResult, error) {
		present := make(map[string]struct{}, len(anchors))
		issues, err := uw.IssueUseCase().GetIssuesByIDs(ctx, anchors)
		if err != nil {
			return publicops.EdgeReadResult{}, err
		}
		for _, issue := range issues {
			if issue != nil {
				present[issue.ID] = struct{}{}
			}
		}
		wisps, err := uw.IssueUseCase().GetWispsByIDs(ctx, anchors)
		if err != nil {
			return publicops.EdgeReadResult{}, err
		}
		for _, wisp := range wisps {
			if wisp != nil {
				present[wisp.ID] = struct{}{}
			}
		}
		edges, err := uw.DependencyUseCase().GetIssueDependencyRecords(ctx, anchors)
		if err != nil {
			return publicops.EdgeReadResult{}, err
		}
		// The type filter and the order run HERE rather than in the reads
		// above, so both implementations narrow and order through one function;
		// a filter pushed into one side's query would put the narrowing in SQL
		// on one backend and in Go on the other.
		return storageissueops.FinishEdgeRead(anchors, present, edges, request.Types), nil
	})
}
