package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// ReadyLister returns the inner store's ready-listing surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner lister unspanned and untimed.
func (s *InstrumentedStorage) ReadyLister() (issueops.ReadyLister, error) {
	inner, err := s.Unwrap().ReadyLister()
	if err != nil {
		return nil, err
	}
	return s.WrapReadyLister(inner), nil
}

// WrapReadyLister instruments guarded ready listings with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapReadyLister(inner issueops.ReadyLister) issueops.ReadyLister {
	return &instrumentedReadyLister{storage: s, inner: inner}
}

type instrumentedReadyLister struct {
	storage *InstrumentedStorage
	inner   issueops.ReadyLister
}

func (l *instrumentedReadyLister) ListReady(ctx context.Context, request issueops.ReadyListRequest) (result issueops.ReadyListing, err error) {
	ctx, span, started := l.storage.op(ctx, "ReadyLister.ListReady")
	result, err = l.inner.ListReady(ctx, request)
	l.storage.done(ctx, span, started, err)
	return result, err
}
