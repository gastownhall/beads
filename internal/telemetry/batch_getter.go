package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BatchGetter returns the inner store's batch-read surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner getter unspanned and untimed.
func (s *InstrumentedStorage) BatchGetter() (issueops.BatchGetter, error) {
	inner, err := s.Unwrap().BatchGetter()
	if err != nil {
		return nil, err
	}
	return s.WrapBatchGetter(inner), nil
}

// WrapBatchGetter instruments guarded batch reads with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapBatchGetter(inner issueops.BatchGetter) issueops.BatchGetter {
	return &instrumentedBatchGetter{storage: s, inner: inner}
}

type instrumentedBatchGetter struct {
	storage *InstrumentedStorage
	inner   issueops.BatchGetter
}

func (c *instrumentedBatchGetter) GetMany(ctx context.Context, request issueops.GetManyRequest) (result issueops.GetManyResult, err error) {
	ctx, span, started := c.storage.op(ctx, "BatchGetter.GetMany")
	result, err = c.inner.GetMany(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
