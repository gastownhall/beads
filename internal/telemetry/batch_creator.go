package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BatchCreator returns the inner store's batch-create surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner creator unspanned and untimed.
func (s *InstrumentedStorage) BatchCreator() (issueops.BatchCreator, error) {
	inner, err := s.Unwrap().BatchCreator()
	if err != nil {
		return nil, err
	}
	return s.WrapBatchCreator(inner), nil
}

// WrapBatchCreator instruments guarded batch creates with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapBatchCreator(inner issueops.BatchCreator) issueops.BatchCreator {
	return &instrumentedBatchCreator{storage: s, inner: inner}
}

type instrumentedBatchCreator struct {
	storage *InstrumentedStorage
	inner   issueops.BatchCreator
}

// CreateBatch is ONE span over the whole request, because the request is the
// transaction: a span per item would time N things that cannot fail or commit
// independently and would hide the only duration anyone can act on.
func (o *instrumentedBatchCreator) CreateBatch(ctx context.Context, request issueops.CreateBatchRequest) (result issueops.CreateBatchResult, err error) {
	ctx, span, started := o.storage.op(ctx, "BatchCreator.CreateBatch")
	result, err = o.inner.CreateBatch(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}
