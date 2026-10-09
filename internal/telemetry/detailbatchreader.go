package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// DetailBatchReader wraps the inner read role in this layer's instrumentation.
func (s *InstrumentedStorage) DetailBatchReader() (issueops.DetailBatchReader, error) {
	inner, err := s.Unwrap().DetailBatchReader()
	if err != nil {
		return nil, err
	}
	return s.WrapDetailBatchReader(inner), nil
}

// WrapDetailBatchReader instruments batch detail reads with the storage meter and tracer.
func (s *InstrumentedStorage) WrapDetailBatchReader(inner issueops.DetailBatchReader) issueops.DetailBatchReader {
	return &instrumentedDetailBatchReader{storage: s, inner: inner}
}

type instrumentedDetailBatchReader struct {
	storage *InstrumentedStorage
	inner   issueops.DetailBatchReader
}

func (c *instrumentedDetailBatchReader) GetBatch(ctx context.Context, request issueops.DetailBatchRequest) (result issueops.DetailBatchResult, err error) {
	ctx, span, started := c.storage.op(ctx, "DetailBatchReader.GetBatch")
	result, err = c.inner.GetBatch(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
