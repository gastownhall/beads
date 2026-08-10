package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// GraphCounter returns the inner store's edge-count surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner counter unspanned and untimed.
func (s *InstrumentedStorage) GraphCounter() (issueops.GraphCounter, error) {
	inner, err := s.Unwrap().GraphCounter()
	if err != nil {
		return nil, err
	}
	return s.WrapGraphCounter(inner), nil
}

// WrapGraphCounter instruments guarded edge counts with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapGraphCounter(inner issueops.GraphCounter) issueops.GraphCounter {
	return &instrumentedGraphCounter{storage: s, inner: inner}
}

type instrumentedGraphCounter struct {
	storage *InstrumentedStorage
	inner   issueops.GraphCounter
}

func (c *instrumentedGraphCounter) CountEdges(ctx context.Context, request issueops.EdgeCountRequest) (result issueops.EdgeCountResult, err error) {
	ctx, span, started := c.storage.op(ctx, "GraphCounter.CountEdges")
	result, err = c.inner.CountEdges(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
