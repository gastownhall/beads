package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// LeaseReclaimer returns the inner store's lease-sweep surface wrapped in
// this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) LeaseReclaimer() (issueops.LeaseReclaimer, error) {
	inner, err := s.Unwrap().LeaseReclaimer()
	if err != nil {
		return nil, err
	}
	return s.WrapLeaseReclaimer(inner), nil
}

// WrapLeaseReclaimer instruments the lease sweep with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapLeaseReclaimer(inner issueops.LeaseReclaimer) issueops.LeaseReclaimer {
	return &instrumentedLeaseReclaimer{storage: s, inner: inner}
}

type instrumentedLeaseReclaimer struct {
	storage *InstrumentedStorage
	inner   issueops.LeaseReclaimer
}

func (c *instrumentedLeaseReclaimer) Reclaim(ctx context.Context, request issueops.ReclaimRequest) (result issueops.ReclaimResult, err error) {
	ctx, span, started := c.storage.op(ctx, "LeaseReclaimer.Reclaim")
	result, err = c.inner.Reclaim(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
