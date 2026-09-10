package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BlockingAnnotator returns the inner store's blocking-decoration surface
// wrapped in this layer's instrumentation. It recurses instead of delegating: a
// blind delegation would return the inner annotator unspanned and untimed.
func (s *InstrumentedStorage) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	inner, err := s.Unwrap().BlockingAnnotator()
	if err != nil {
		return nil, err
	}
	return s.WrapBlockingAnnotator(inner), nil
}

// WrapBlockingAnnotator instruments guarded blocking annotations with this
// storage layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapBlockingAnnotator(inner issueops.BlockingAnnotator) issueops.BlockingAnnotator {
	return &instrumentedBlockingAnnotator{storage: s, inner: inner}
}

type instrumentedBlockingAnnotator struct {
	storage *InstrumentedStorage
	inner   issueops.BlockingAnnotator
}

func (a *instrumentedBlockingAnnotator) AnnotateBlocking(ctx context.Context, request issueops.BlockingRequest) (result issueops.BlockingResult, err error) {
	ctx, span, started := a.storage.op(ctx, "BlockingAnnotator.AnnotateBlocking")
	result, err = a.inner.AnnotateBlocking(ctx, request)
	a.storage.done(ctx, span, started, err)
	return result, err
}
