package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// StatsReporter returns the inner store's summary-statistics surface wrapped in
// this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner reporter unspanned and untimed.
func (s *InstrumentedStorage) StatsReporter() (issueops.StatsReporter, error) {
	inner, err := s.Unwrap().StatsReporter()
	if err != nil {
		return nil, err
	}
	return s.WrapStatsReporter(inner), nil
}

// WrapStatsReporter instruments guarded summary statistics with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapStatsReporter(inner issueops.StatsReporter) issueops.StatsReporter {
	return &instrumentedStatsReporter{storage: s, inner: inner}
}

type instrumentedStatsReporter struct {
	storage *InstrumentedStorage
	inner   issueops.StatsReporter
}

func (r *instrumentedStatsReporter) Stats(ctx context.Context, request issueops.StatsRequest) (result issueops.StatsResult, err error) {
	ctx, span, started := r.storage.op(ctx, "StatsReporter.Stats")
	result, err = r.inner.Stats(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedStatsReporter) AssigneeStats(ctx context.Context, request issueops.AssigneeStatsRequest) (result issueops.StatsResult, err error) {
	ctx, span, started := r.storage.op(ctx, "StatsReporter.AssigneeStats")
	result, err = r.inner.AssigneeStats(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}
