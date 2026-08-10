package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Sweeper returns the inner store's bulk-clearance surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner sweeper unspanned and untimed — and this
// is the longest-running write on the surface, so it is the one whose span
// matters most.
func (s *InstrumentedStorage) Sweeper() (issueops.Sweeper, error) {
	inner, err := s.Unwrap().Sweeper()
	if err != nil {
		return nil, err
	}
	return s.WrapSweeper(inner), nil
}

// WrapSweeper instruments guarded bulk clearance with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapSweeper(inner issueops.Sweeper) issueops.Sweeper {
	return &instrumentedSweeper{storage: s, inner: inner}
}

type instrumentedSweeper struct {
	storage *InstrumentedStorage
	inner   issueops.Sweeper
}

func (c *instrumentedSweeper) Sweep(ctx context.Context, request issueops.SweepRequest) (result issueops.SweepResult, err error) {
	ctx, span, started := c.storage.op(ctx, "Sweeper.Sweep")
	result, err = c.inner.Sweep(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
