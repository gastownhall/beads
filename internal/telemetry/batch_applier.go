package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BatchApplier returns the inner store's apply-many surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) BatchApplier() (issueops.BatchApplier, error) {
	inner, err := s.Unwrap().BatchApplier()
	if err != nil {
		return nil, err
	}
	return s.WrapBatchApplier(inner), nil
}

// WrapBatchApplier instruments apply-many with this storage layer's existing
// telemetry meter and tracer.
func (s *InstrumentedStorage) WrapBatchApplier(inner issueops.BatchApplier) issueops.BatchApplier {
	return &instrumentedBatchApplier{storage: s, inner: inner}
}

type instrumentedBatchApplier struct {
	storage *InstrumentedStorage
	inner   issueops.BatchApplier
}

// ApplyBatch spans the whole request, which is the right unit for this role:
// the request IS the transaction, so a per-item span would report four latency
// distributions for one commit.
func (o *instrumentedBatchApplier) ApplyBatch(ctx context.Context, request issueops.ApplyBatchRequest) (result issueops.ApplyBatchResult, err error) {
	ctx, span, started := o.storage.op(ctx, "BatchApplier.ApplyBatch")
	result, err = o.inner.ApplyBatch(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}
