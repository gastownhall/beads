package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// IssueReader returns the inner store's query surface wrapped in this layer's
// instrumentation. It recurses instead of delegating: a blind delegation would
// return the inner reader unspanned and untimed.
func (s *InstrumentedStorage) IssueReader() (issueops.Reader, error) {
	inner, err := s.Unwrap().IssueReader()
	if err != nil {
		return nil, err
	}
	return s.WrapIssueReader(inner), nil
}

// WrapIssueReader instruments guarded public issue queries with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapIssueReader(inner issueops.Reader) issueops.Reader {
	return &instrumentedIssueReader{storage: s, inner: inner}
}

type instrumentedIssueReader struct {
	storage *InstrumentedStorage
	inner   issueops.Reader
}

func (r *instrumentedIssueReader) Ready(ctx context.Context, request issueops.ReadyRequest) (result issueops.IssuePage, err error) {
	ctx, span, started := r.storage.op(ctx, "IssueReader.Ready")
	result, err = r.inner.Ready(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedIssueReader) List(ctx context.Context, request issueops.ListRequest) (result issueops.IssuePage, err error) {
	ctx, span, started := r.storage.op(ctx, "IssueReader.List")
	result, err = r.inner.List(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedIssueReader) Get(ctx context.Context, request issueops.GetRequest) (result *issueops.IssueDetails, err error) {
	ctx, span, started := r.storage.op(ctx, "IssueReader.Get")
	result, err = r.inner.Get(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}
