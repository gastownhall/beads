package uow

import (
	"context"
	"fmt"

	publicops "github.com/steveyegge/beads/issueops"
)

// DetailBatchReaderSource publishes the batch detail capability on a provider.
type DetailBatchReaderSource interface {
	DetailBatchReader() (publicops.DetailBatchReader, error)
}

type detailBatchReader struct{ provider UnitOfWorkProvider }

func (p *doltSQLProvider) DetailBatchReader() (publicops.DetailBatchReader, error) {
	return NewDetailBatchReader(p)
}

// NewDetailBatchReader constructs batch detail reads backed by provider.
func NewDetailBatchReader(provider UnitOfWorkProvider) (publicops.DetailBatchReader, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new detail batch reader: unit-of-work provider must not be nil")
	}
	return &detailBatchReader{provider: provider}, nil
}

var _ publicops.DetailBatchReader = (*detailBatchReader)(nil)

func (r *detailBatchReader) GetBatch(ctx context.Context, request publicops.DetailBatchRequest) (publicops.DetailBatchResult, error) {
	if err := publicops.ValidateDetailBatchRequest(request); err != nil {
		return publicops.DetailBatchResult{}, err
	}
	if len(request.IDs) == 0 {
		return publicops.DetailBatchResult{Items: []publicops.DetailBatchItem{}}, nil
	}
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (publicops.DetailBatchResult, error) {
		return uw.IssueUseCase().GetDetailBatch(ctx, request)
	})
}
