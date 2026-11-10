package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// CycleDetector returns the inner store's cycle-report surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind delegation
// would return the inner detector unspanned and untimed.
func (s *InstrumentedStorage) CycleDetector() (issueops.CycleDetector, error) {
	inner, err := s.Unwrap().CycleDetector()
	if err != nil {
		return nil, err
	}
	return s.WrapCycleDetector(inner), nil
}

// WrapCycleDetector instruments guarded cycle reports with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapCycleDetector(inner issueops.CycleDetector) issueops.CycleDetector {
	return &instrumentedCycleDetector{storage: s, inner: inner}
}

type instrumentedCycleDetector struct {
	storage *InstrumentedStorage
	inner   issueops.CycleDetector
}

func (c *instrumentedCycleDetector) DetectCycles(ctx context.Context, request issueops.DetectCyclesRequest) (result issueops.CycleReport, err error) {
	ctx, span, started := c.storage.op(ctx, "CycleDetector.DetectCycles")
	result, err = c.inner.DetectCycles(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
