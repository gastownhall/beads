package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Deleter returns the inner store's named-row erasure surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner deleter unspanned and untimed, and on a
// destructive operation the span is what tells an operator which request erased
// what and how long it held the write transaction.
func (s *InstrumentedStorage) Deleter() (issueops.Deleter, error) {
	inner, err := s.Unwrap().Deleter()
	if err != nil {
		return nil, err
	}
	return s.WrapDeleter(inner), nil
}

// WrapDeleter instruments named-row erasure with this storage layer's existing
// telemetry meter and tracer.
func (s *InstrumentedStorage) WrapDeleter(inner issueops.Deleter) issueops.Deleter {
	return &instrumentedDeleter{storage: s, inner: inner}
}

type instrumentedDeleter struct {
	storage *InstrumentedStorage
	inner   issueops.Deleter
}

func (c *instrumentedDeleter) Delete(ctx context.Context, request issueops.DeleteRequest) (result issueops.DeleteResult, err error) {
	ctx, span, started := c.storage.op(ctx, "Deleter.Delete")
	result, err = c.inner.Delete(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
