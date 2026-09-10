package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// ReadyClaimer returns the inner store's claim surface wrapped in this layer's
// instrumentation. It recurses instead of delegating: a blind delegation would
// return the inner claimer unspanned and untimed.
func (s *InstrumentedStorage) ReadyClaimer() (issueops.ReadyClaimer, error) {
	inner, err := s.Unwrap().ReadyClaimer()
	if err != nil {
		return nil, err
	}
	return s.WrapReadyClaimer(inner), nil
}

// WrapReadyClaimer instruments guarded ready claims with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapReadyClaimer(inner issueops.ReadyClaimer) issueops.ReadyClaimer {
	return &instrumentedReadyClaimer{storage: s, inner: inner}
}

type instrumentedReadyClaimer struct {
	storage *InstrumentedStorage
	inner   issueops.ReadyClaimer
}

func (c *instrumentedReadyClaimer) ClaimNext(ctx context.Context, request issueops.ClaimNextRequest) (result issueops.ClaimNextResult, err error) {
	ctx, span, started := c.storage.op(ctx, "ReadyClaimer.ClaimNext")
	result, err = c.inner.ClaimNext(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
