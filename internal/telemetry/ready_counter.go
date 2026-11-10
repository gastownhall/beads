package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// ReadyCounter returns the inner store's ready-count surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner counter unspanned and untimed.
func (s *InstrumentedStorage) ReadyCounter() (issueops.ReadyCounter, error) {
	inner, err := s.Unwrap().ReadyCounter()
	if err != nil {
		return nil, err
	}
	return s.WrapReadyCounter(inner), nil
}

// WrapReadyCounter instruments guarded ready counts with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapReadyCounter(inner issueops.ReadyCounter) issueops.ReadyCounter {
	return &instrumentedReadyCounter{storage: s, inner: inner}
}

type instrumentedReadyCounter struct {
	storage *InstrumentedStorage
	inner   issueops.ReadyCounter
}

func (c *instrumentedReadyCounter) CountReady(ctx context.Context, request issueops.ReadyRequest) (result issueops.ReadyCountResult, err error) {
	ctx, span, started := c.storage.op(ctx, "ReadyCounter.CountReady")
	result, err = c.inner.CountReady(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
