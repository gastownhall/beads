package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// InitVerifier returns the inner store's identity-read surface wrapped in this
// layer's instrumentation. There is no read/write distinction here — telemetry
// spans reads too.
func (s *InstrumentedStorage) InitVerifier() (issueops.InitVerifier, error) {
	inner, err := s.Unwrap().InitVerifier()
	if err != nil {
		return nil, err
	}
	return s.WrapInitVerifier(inner), nil
}

// WrapInitVerifier instruments guarded identity reads with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapInitVerifier(inner issueops.InitVerifier) issueops.InitVerifier {
	return &instrumentedInitVerifier{storage: s, inner: inner}
}

type instrumentedInitVerifier struct {
	storage *InstrumentedStorage
	inner   issueops.InitVerifier
}

func (v *instrumentedInitVerifier) VerifyIdentity(ctx context.Context, request issueops.VerifyIdentityRequest) (result issueops.VerifyIdentityResult, err error) {
	ctx, span, started := v.storage.op(ctx, "InitVerifier.VerifyIdentity")
	result, err = v.inner.VerifyIdentity(ctx, request)
	v.storage.done(ctx, span, started, err)
	return result, err
}
