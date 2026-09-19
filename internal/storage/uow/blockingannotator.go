package uow

import (
	"context"
	"fmt"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// BlockingAnnotatorSource is the capability accessor a unit-of-work provider
// offers for the blocking-decoration role, the sibling of EdgeReaderSource.
type BlockingAnnotatorSource interface {
	BlockingAnnotator() (publicops.BlockingAnnotator, error)
}

// blockingAnnotator answers blocking annotations through a unit of work.
type blockingAnnotator struct {
	provider UnitOfWorkProvider
}

// BlockingAnnotator returns the guarded blocking-decoration surface for this
// provider.
func (p *doltSQLProvider) BlockingAnnotator() (publicops.BlockingAnnotator, error) {
	return NewBlockingAnnotator(p)
}

// NewBlockingAnnotator constructs public blocking annotations backed by
// provider.
func NewBlockingAnnotator(provider UnitOfWorkProvider) (publicops.BlockingAnnotator, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new blocking annotator: unit-of-work provider must not be nil")
	}
	return &blockingAnnotator{provider: provider}, nil
}

var _ publicops.BlockingAnnotator = (*blockingAnnotator)(nil)

// AnnotateBlocking answers one blocking read inside ONE unit of work, so the
// two edge reads and the status lookups behind them see one snapshot — the
// property the two store-backed bodies get from a shared read transaction.
//
// This is the genuinely separate body: the store side partitions the ids by
// plane and reads each anchor's outbound edges from the tier it lives on, while
// the use case below reads BOTH tiers for every id and merges. The three maps
// are the same three maps, and FinishBlockingAnnotation is what makes the SHAPE
// of the answer — the entry per id, the pinned order, the collapse of repeats —
// one decision rather than two.
func (a *blockingAnnotator) AnnotateBlocking(ctx context.Context, request publicops.BlockingRequest) (publicops.BlockingResult, error) {
	if err := storageissueops.ValidateBlockingRequest(request); err != nil {
		return publicops.BlockingResult{}, err
	}
	anchors := storageissueops.EdgeReadAnchors(request.IDs)
	if len(anchors) == 0 {
		return publicops.BlockingResult{Items: []publicops.IssueBlocking{}}, nil
	}
	return RunTxRead(ctx, a.provider, func(ctx context.Context, uw UnitOfWork) (publicops.BlockingResult, error) {
		info, err := uw.DependencyUseCase().GetBlockingInfo(ctx, anchors)
		if err != nil {
			return publicops.BlockingResult{}, err
		}
		return storageissueops.FinishBlockingAnnotation(anchors, info.BlockedBy, info.Blocks, info.Parent), nil
	})
}
