package telemetry

import (
	"context"

	"github.com/steveyegge/beads/memoryops"
)

// Memories returns the inner store's persistent-memory surface wrapped in this
// layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) Memories() (memoryops.Memories, error) {
	inner, err := s.Unwrap().Memories()
	if err != nil {
		return nil, err
	}
	return s.WrapMemories(inner), nil
}

// WrapMemories instruments guarded persistent-memory access with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapMemories(inner memoryops.Memories) memoryops.Memories {
	return &instrumentedMemories{storage: s, inner: inner}
}

type instrumentedMemories struct {
	storage *InstrumentedStorage
	inner   memoryops.Memories
}

func (m *instrumentedMemories) Remember(ctx context.Context, request memoryops.RememberRequest) (result memoryops.RememberResult, err error) {
	ctx, span, started := m.storage.op(ctx, "Memories.Remember")
	result, err = m.inner.Remember(ctx, request)
	m.storage.done(ctx, span, started, err)
	return result, err
}

func (m *instrumentedMemories) Recall(ctx context.Context, request memoryops.RecallRequest) (result memoryops.RecallResult, err error) {
	ctx, span, started := m.storage.op(ctx, "Memories.Recall")
	result, err = m.inner.Recall(ctx, request)
	m.storage.done(ctx, span, started, err)
	return result, err
}

func (m *instrumentedMemories) Forget(ctx context.Context, request memoryops.ForgetRequest) (result memoryops.ForgetResult, err error) {
	ctx, span, started := m.storage.op(ctx, "Memories.Forget")
	result, err = m.inner.Forget(ctx, request)
	m.storage.done(ctx, span, started, err)
	return result, err
}

func (m *instrumentedMemories) List(ctx context.Context, request memoryops.ListRequest) (result memoryops.ListResult, err error) {
	ctx, span, started := m.storage.op(ctx, "Memories.List")
	result, err = m.inner.List(ctx, request)
	m.storage.done(ctx, span, started, err)
	return result, err
}
