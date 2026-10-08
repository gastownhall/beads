package uow

import (
	"context"
	"fmt"

	publicops "github.com/steveyegge/beads/issueops"
)

// BatchGetterSource is the capability accessor a unit-of-work provider offers
// for the batch-read role, the sibling of CounterSource and GraphCounterSource.
type BatchGetterSource interface {
	BatchGetter() (publicops.BatchGetter, error)
}

// batchGetter answers a GetMany through a unit of work.
type batchGetter struct {
	provider UnitOfWorkProvider
}

// BatchGetter returns the guarded batch-read surface for this provider.
func (p *doltSQLProvider) BatchGetter() (publicops.BatchGetter, error) {
	return NewBatchGetter(p)
}

// NewBatchGetter constructs a public BatchGetter backed by provider.
func NewBatchGetter(provider UnitOfWorkProvider) (publicops.BatchGetter, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new batch getter: unit-of-work provider must not be nil")
	}
	return &batchGetter{provider: provider}, nil
}

var _ publicops.BatchGetter = (*batchGetter)(nil)

// GetMany reaches domain.IssueSQLRepository.GetMany inside ONE read-only unit
// of work, the same seam CountEdges reaches DependencyUseCase.CountEdges
// through: the request's whole vocabulary is validated inside the shared
// body (issueops.ExecuteGetMany), so this leg does no pre-check of its own.
func (c *batchGetter) GetMany(ctx context.Context, request publicops.GetManyRequest) (publicops.GetManyResult, error) {
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.GetManyResult, error) {
		return uw.IssueUseCase().GetMany(ctx, request)
	})
}
