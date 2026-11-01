package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Heartbeater returns the inner store's lease-refresh surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) Heartbeater() (issueops.Heartbeater, error) {
	inner, err := s.Unwrap().Heartbeater()
	if err != nil {
		return nil, err
	}
	return s.WrapHeartbeater(inner), nil
}

// WrapHeartbeater instruments the lease refresh with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapHeartbeater(inner issueops.Heartbeater) issueops.Heartbeater {
	return &instrumentedHeartbeater{storage: s, inner: inner}
}

type instrumentedHeartbeater struct {
	storage *InstrumentedStorage
	inner   issueops.Heartbeater
}

func (c *instrumentedHeartbeater) Heartbeat(ctx context.Context, request issueops.HeartbeatRequest) (result issueops.HeartbeatResult, err error) {
	ctx, span, started := c.storage.op(ctx, "Heartbeater.Heartbeat")
	result, err = c.inner.Heartbeat(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
