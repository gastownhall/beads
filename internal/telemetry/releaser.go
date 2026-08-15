package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Releaser returns the inner store's claim-release surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) Releaser() (issueops.Releaser, error) {
	inner, err := s.Unwrap().Releaser()
	if err != nil {
		return nil, err
	}
	return s.WrapReleaser(inner), nil
}

// WrapReleaser instruments claim releases with this storage layer's existing
// telemetry meter and tracer.
func (s *InstrumentedStorage) WrapReleaser(inner issueops.Releaser) issueops.Releaser {
	return &instrumentedReleaser{storage: s, inner: inner}
}

type instrumentedReleaser struct {
	storage *InstrumentedStorage
	inner   issueops.Releaser
}

// Release spans the release. Its refusals ARE errors here, unlike a lost
// compare-and-set: a caller that asked to release a claim it does not hold has
// been told no, and a fleet operator watching this span wants that visible
// rather than folded into the success rate.
func (r *instrumentedReleaser) Release(ctx context.Context, request issueops.ReleaseRequest) (result issueops.ReleaseResult, err error) {
	ctx, span, started := r.storage.op(ctx, "Releaser.Release")
	result, err = r.inner.Release(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}
