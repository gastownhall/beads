package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// TreeWalker returns the inner store's dependency-tree surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind delegation
// would return the inner walker unspanned and untimed.
func (s *InstrumentedStorage) TreeWalker() (issueops.TreeWalker, error) {
	inner, err := s.Unwrap().TreeWalker()
	if err != nil {
		return nil, err
	}
	return s.WrapTreeWalker(inner), nil
}

// WrapTreeWalker instruments guarded dependency-tree walks with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapTreeWalker(inner issueops.TreeWalker) issueops.TreeWalker {
	return &instrumentedTreeWalker{storage: s, inner: inner}
}

type instrumentedTreeWalker struct {
	storage *InstrumentedStorage
	inner   issueops.TreeWalker
}

func (t *instrumentedTreeWalker) WalkTree(ctx context.Context, request issueops.WalkTreeRequest) (result issueops.TreeResult, err error) {
	ctx, span, started := t.storage.op(ctx, "TreeWalker.WalkTree")
	result, err = t.inner.WalkTree(ctx, request)
	t.storage.done(ctx, span, started, err)
	return result, err
}
