package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Counter returns the inner store's count surface wrapped in this layer's
// instrumentation. It recurses instead of delegating: a blind delegation would
// return the inner counter unspanned and untimed.
func (s *InstrumentedStorage) Counter() (issueops.Counter, error) {
	inner, err := s.Unwrap().Counter()
	if err != nil {
		return nil, err
	}
	return s.WrapCounter(inner), nil
}

// WrapCounter instruments guarded issue counts with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapCounter(inner issueops.Counter) issueops.Counter {
	return &instrumentedCounter{storage: s, inner: inner}
}

type instrumentedCounter struct {
	storage *InstrumentedStorage
	inner   issueops.Counter
}

func (c *instrumentedCounter) Count(ctx context.Context, request issueops.CountRequest) (result issueops.CountResult, err error) {
	ctx, span, started := c.storage.op(ctx, "Counter.Count")
	result, err = c.inner.Count(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}

func (c *instrumentedCounter) CountByGroup(ctx context.Context, request issueops.CountByGroupRequest) (result issueops.CountByGroupResult, err error) {
	ctx, span, started := c.storage.op(ctx, "Counter.CountByGroup")
	result, err = c.inner.CountByGroup(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
