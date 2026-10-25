package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BlockedLister returns the inner store's blocked-listing surface wrapped in
// this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) BlockedLister() (issueops.BlockedLister, error) {
	inner, err := s.Unwrap().BlockedLister()
	if err != nil {
		return nil, err
	}
	return s.WrapBlockedLister(inner), nil
}

// WrapBlockedLister instruments the blocked listing with this storage layer's
// existing telemetry meter and tracer. The external-dependency decorator
// calls it on the lister it builds over itself, so the span survives that
// override.
func (s *InstrumentedStorage) WrapBlockedLister(inner issueops.BlockedLister) issueops.BlockedLister {
	return &instrumentedBlockedLister{storage: s, inner: inner}
}

type instrumentedBlockedLister struct {
	storage *InstrumentedStorage
	inner   issueops.BlockedLister
}

func (c *instrumentedBlockedLister) ListBlocked(ctx context.Context, request issueops.BlockedRequest) (result issueops.BlockedResult, err error) {
	ctx, span, started := c.storage.op(ctx, "BlockedLister.ListBlocked")
	result, err = c.inner.ListBlocked(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
