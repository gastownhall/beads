package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Querier returns the inner store's boolean-query surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner querier unspanned and untimed.
func (s *InstrumentedStorage) Querier() (issueops.Querier, error) {
	inner, err := s.Unwrap().Querier()
	if err != nil {
		return nil, err
	}
	return s.WrapQuerier(inner), nil
}

// WrapQuerier instruments guarded boolean queries with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapQuerier(inner issueops.Querier) issueops.Querier {
	return &instrumentedQuerier{storage: s, inner: inner}
}

type instrumentedQuerier struct {
	storage *InstrumentedStorage
	inner   issueops.Querier
}

func (q *instrumentedQuerier) Query(ctx context.Context, request issueops.QueryRequest) (result issueops.IssuePage, err error) {
	ctx, span, started := q.storage.op(ctx, "Querier.Query")
	result, err = q.inner.Query(ctx, request)
	q.storage.done(ctx, span, started, err)
	return result, err
}
