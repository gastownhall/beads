package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// EdgeReader returns the inner store's stored-edge surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner reader unspanned and untimed.
func (s *InstrumentedStorage) EdgeReader() (issueops.EdgeReader, error) {
	inner, err := s.Unwrap().EdgeReader()
	if err != nil {
		return nil, err
	}
	return s.WrapEdgeReader(inner), nil
}

// WrapEdgeReader instruments guarded stored-edge reads with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapEdgeReader(inner issueops.EdgeReader) issueops.EdgeReader {
	return &instrumentedEdgeReader{storage: s, inner: inner}
}

type instrumentedEdgeReader struct {
	storage *InstrumentedStorage
	inner   issueops.EdgeReader
}

func (r *instrumentedEdgeReader) ReadEdges(ctx context.Context, request issueops.EdgeReadRequest) (result issueops.EdgeReadResult, err error) {
	ctx, span, started := r.storage.op(ctx, "EdgeReader.ReadEdges")
	result, err = r.inner.ReadEdges(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}
