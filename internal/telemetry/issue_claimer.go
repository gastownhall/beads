package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// IssueClaimer returns the inner store's claimer wrapped in this layer's
// instrumentation. It recurses instead of delegating: a blind delegation would
// return the inner claimer unspanned and untimed.
func (s *InstrumentedStorage) IssueClaimer() (issueops.Claimer, error) {
	inner, err := s.Unwrap().IssueClaimer()
	if err != nil {
		return nil, err
	}
	return s.WrapIssueClaimer(inner), nil
}

// WrapIssueClaimer instruments the guarded public claim with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapIssueClaimer(inner issueops.Claimer) issueops.Claimer {
	return &instrumentedIssueClaimer{storage: s, inner: inner}
}

type instrumentedIssueClaimer struct {
	storage *InstrumentedStorage
	inner   issueops.Claimer
}

func (c *instrumentedIssueClaimer) Claim(ctx context.Context, request issueops.ClaimRequest) (result issueops.ClaimResult, err error) {
	ctx, span, started := c.storage.op(ctx, "IssueClaimer.Claim")
	result, err = c.inner.Claim(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
