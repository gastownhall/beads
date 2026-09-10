package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// DependencyEditor returns the inner store's dependency-edge surface wrapped
// in this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner editor unspanned and untimed.
func (s *InstrumentedStorage) DependencyEditor() (issueops.DependencyEditor, error) {
	inner, err := s.Unwrap().DependencyEditor()
	if err != nil {
		return nil, err
	}
	return s.WrapDependencyEditor(inner), nil
}

// WrapDependencyEditor instruments guarded dependency edits with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapDependencyEditor(inner issueops.DependencyEditor) issueops.DependencyEditor {
	return &instrumentedDependencyEditor{storage: s, inner: inner}
}

type instrumentedDependencyEditor struct {
	storage *InstrumentedStorage
	inner   issueops.DependencyEditor
}

// AddDependencies is ONE span over the whole request, because the request is
// the transaction: a span per edge would time N things that cannot fail or
// commit independently.
func (e *instrumentedDependencyEditor) AddDependencies(ctx context.Context, request issueops.AddDependenciesRequest) (result issueops.AddDependenciesResult, err error) {
	ctx, span, started := e.storage.op(ctx, "DependencyEditor.AddDependencies")
	result, err = e.inner.AddDependencies(ctx, request)
	e.storage.done(ctx, span, started, err)
	return result, err
}

func (e *instrumentedDependencyEditor) RemoveDependency(ctx context.Context, request issueops.RemoveDependencyRequest) (result issueops.RemoveDependencyResult, err error) {
	ctx, span, started := e.storage.op(ctx, "DependencyEditor.RemoveDependency")
	result, err = e.inner.RemoveDependency(ctx, request)
	e.storage.done(ctx, span, started, err)
	return result, err
}
