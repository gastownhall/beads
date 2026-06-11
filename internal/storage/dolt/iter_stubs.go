// Package dolt — iter_stubs.go
//
// Slice-wrapping stubs for the Iter* methods whose fully streaming
// implementation has not landed yet. The interface ships complete now
// (be-jaavsb / be-yinl4d); each stub will be replaced by a fully
// streaming implementation in a follow-up child of be-yinl4d. The TODO
// comment names the tracking bead so reviewers can find the work item.
package dolt

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IterIssueComments streams comments on an issue.
//
// TODO(be-yinl4d-iter): replace slice-then-walk with a fully streaming
// implementation. Tracked under be-7hvi6c (or its successor child).
func (s *DoltStore) IterIssueComments(ctx context.Context, issueID string) (storage.Iter[types.Comment], error) {
	cs, err := s.GetIssueComments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(cs), nil
}

// IterAttachments streams attachment metadata on an issue.
//
// TODO(be-yinl4d-iter): replace slice-then-walk with a fully streaming
// implementation.
func (s *DoltStore) IterAttachments(ctx context.Context, issueID string) (storage.Iter[types.Attachment], error) {
	as, err := s.ListAttachments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(as), nil
}

// IterEvents streams the audit-trail events for an issue.
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *DoltStore) IterEvents(ctx context.Context, issueID string, limit int) (storage.Iter[types.Event], error) {
	ev, err := s.GetEvents(ctx, issueID, limit)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ev), nil
}

// IterAllEventsSince streams every audit-trail event newer than `since`.
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *DoltStore) IterAllEventsSince(ctx context.Context, since time.Time) (storage.Iter[types.Event], error) {
	ev, err := s.GetAllEventsSince(ctx, since)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ev), nil
}

// IterReadyWork streams ready-work issues.
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *DoltStore) IterReadyWork(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.Issue], error) {
	is, err := s.GetReadyWork(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(is), nil
}

// IterBlockedIssues streams blocked issues.
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *DoltStore) IterBlockedIssues(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.BlockedIssue], error) {
	bs, err := s.GetBlockedIssues(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(bs), nil
}

// IterWisps streams ephemeral issues matching the filter.
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *DoltStore) IterWisps(ctx context.Context, filter types.WispFilter) (storage.Iter[types.Issue], error) {
	ws, err := s.ListWisps(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ws), nil
}

// IterAllDependencyRecords streams every dependency edge as a flat
// sequence of *types.Dependency rows. Stub-then-slice; follow-up child
// of be-yinl4d will replace this with a streaming implementation.
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *DoltStore) IterAllDependencyRecords(ctx context.Context) (storage.Iter[types.Dependency], error) {
	all, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}
	// Flatten the map[string][]*types.Dependency back into a slice. The
	// streaming impl will read directly from the dependencies table in a
	// single query without the map round-trip.
	var flat []*types.Dependency
	for _, deps := range all {
		flat = append(flat, deps...)
	}
	return storage.NewSliceIter(flat), nil
}
