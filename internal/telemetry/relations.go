package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// IssueRelations returns the inner store's neighbor-query surface wrapped in
// this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) IssueRelations() (issueops.Relations, error) {
	inner, err := s.Unwrap().IssueRelations()
	if err != nil {
		return nil, err
	}
	return s.WrapIssueRelations(inner), nil
}

// WrapIssueRelations instruments guarded neighbor queries with this storage
// layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapIssueRelations(inner issueops.Relations) issueops.Relations {
	return &instrumentedIssueRelations{storage: s, inner: inner}
}

type instrumentedIssueRelations struct {
	storage *InstrumentedStorage
	inner   issueops.Relations
}

func (r *instrumentedIssueRelations) Related(ctx context.Context, request issueops.RelatedRequest) (result []*issueops.RelatedIssue, err error) {
	ctx, span, started := r.storage.op(ctx, "IssueRelations.Related")
	result, err = r.inner.Related(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}
