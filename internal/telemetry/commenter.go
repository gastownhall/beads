package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// Commenter returns the inner store's add-comment surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner commenter unspanned and untimed.
func (s *InstrumentedStorage) Commenter() (issueops.Commenter, error) {
	inner, err := s.Unwrap().Commenter()
	if err != nil {
		return nil, err
	}
	return s.WrapCommenter(inner), nil
}

// WrapCommenter instruments guarded comment writes with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapCommenter(inner issueops.Commenter) issueops.Commenter {
	return &instrumentedCommenter{storage: s, inner: inner}
}

type instrumentedCommenter struct {
	storage *InstrumentedStorage
	inner   issueops.Commenter
}

func (c *instrumentedCommenter) AddComment(ctx context.Context, request issueops.AddCommentRequest) (result issueops.AddCommentResult, err error) {
	ctx, span, started := c.storage.op(ctx, "Commenter.AddComment")
	result, err = c.inner.AddComment(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
