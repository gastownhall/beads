package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// MetadataCAS returns the inner store's conditional metadata write wrapped in
// this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) MetadataCAS() (issueops.MetadataCAS, error) {
	inner, err := s.Unwrap().MetadataCAS()
	if err != nil {
		return nil, err
	}
	return s.WrapMetadataCAS(inner), nil
}

// WrapMetadataCAS instruments conditional metadata writes with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapMetadataCAS(inner issueops.MetadataCAS) issueops.MetadataCAS {
	return &instrumentedMetadataCAS{storage: s, inner: inner}
}

type instrumentedMetadataCAS struct {
	storage *InstrumentedStorage
	inner   issueops.MetadataCAS
}

// CompareAndSetKey spans the swap. A LOST RACE IS NOT AN ERROR here either, so
// the span it closes is a successful one: a caller looping on a contended key
// would otherwise paint its own contention as a failure rate.
func (c *instrumentedMetadataCAS) CompareAndSetKey(ctx context.Context, request issueops.CompareAndSetKeyRequest) (result issueops.CompareAndSetKeyResult, err error) {
	ctx, span, started := c.storage.op(ctx, "MetadataCAS.CompareAndSetKey")
	result, err = c.inner.CompareAndSetKey(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
