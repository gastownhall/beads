package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BatchCloser returns the inner store's batch-close surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner closer unspanned and untimed.
func (s *InstrumentedStorage) BatchCloser() (issueops.BatchCloser, error) {
	inner, err := s.Unwrap().BatchCloser()
	if err != nil {
		return nil, err
	}
	return s.WrapBatchCloser(inner), nil
}

// WrapBatchCloser instruments guarded batch closes with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapBatchCloser(inner issueops.BatchCloser) issueops.BatchCloser {
	return &instrumentedBatchCloser{storage: s, inner: inner}
}

type instrumentedBatchCloser struct {
	storage *InstrumentedStorage
	inner   issueops.BatchCloser
}

// CloseBatch is ONE span over the whole request, because the request is the
// transaction: a span per item would time N things that cannot fail or commit
// independently and would hide the only duration anyone can act on.
func (o *instrumentedBatchCloser) CloseBatch(ctx context.Context, request issueops.CloseBatchRequest) (result issueops.CloseBatchResult, err error) {
	ctx, span, started := o.storage.op(ctx, "BatchCloser.CloseBatch")
	result, err = o.inner.CloseBatch(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}
