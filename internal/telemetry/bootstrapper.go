package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Bootstrapper returns the inner store's identity-seeding surface wrapped in
// this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) Bootstrapper() (issueops.Bootstrapper, error) {
	inner, err := s.Unwrap().Bootstrapper()
	if err != nil {
		return nil, err
	}
	return s.WrapBootstrapper(inner), nil
}

// WrapBootstrapper instruments guarded identity seeding with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapBootstrapper(inner issueops.Bootstrapper) issueops.Bootstrapper {
	return &instrumentedBootstrapper{storage: s, inner: inner}
}

type instrumentedBootstrapper struct {
	storage *InstrumentedStorage
	inner   issueops.Bootstrapper
}

func (b *instrumentedBootstrapper) Bootstrap(ctx context.Context, request issueops.BootstrapRequest) (result issueops.BootstrapResult, err error) {
	ctx, span, started := b.storage.op(ctx, "Bootstrapper.Bootstrap")
	result, err = b.inner.Bootstrap(ctx, request)
	b.storage.done(ctx, span, started, err)
	return result, err
}
