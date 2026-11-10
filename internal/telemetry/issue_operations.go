package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// IssueLifecycle returns the inner store's lifecycle wrapped in this layer's
// instrumentation. It recurses instead of delegating: a blind delegation would
// return the inner lifecycle unspanned and untimed.
func (s *InstrumentedStorage) IssueLifecycle() (issueops.Lifecycle, error) {
	inner, err := s.Unwrap().IssueLifecycle()
	if err != nil {
		return nil, err
	}
	return s.WrapIssueOperations(inner), nil
}

// WrapIssueOperations instruments guarded public issue operations with this
// storage layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapIssueOperations(inner issueops.Lifecycle) issueops.Lifecycle {
	return &instrumentedIssueOperations{storage: s, inner: inner}
}

type instrumentedIssueOperations struct {
	storage *InstrumentedStorage
	inner   issueops.Lifecycle
}

func (o *instrumentedIssueOperations) Create(ctx context.Context, request issueops.CreateRequest) (result issueops.CreateResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Create")
	result, err = o.inner.Create(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}

func (o *instrumentedIssueOperations) Update(ctx context.Context, request issueops.UpdateRequest) (result issueops.UpdateResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Update")
	result, err = o.inner.Update(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}

func (o *instrumentedIssueOperations) Close(ctx context.Context, request issueops.CloseRequest) (result issueops.CloseResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Close")
	result, err = o.inner.Close(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}

func (o *instrumentedIssueOperations) Reopen(ctx context.Context, request issueops.ReopenRequest) (result issueops.ReopenResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Reopen")
	result, err = o.inner.Reopen(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}
