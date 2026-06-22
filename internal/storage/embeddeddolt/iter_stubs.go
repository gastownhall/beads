//go:build cgo

// Package embeddeddolt — iter_stubs.go
//
// Slice-wrapping stubs for the Iter* methods. The embedded Dolt backend
// uses a per-method short-lived connection model (`withConn`) which is
// incompatible with the dedicated-conn cursor pattern used by the
// streaming iterators in internal/storage/dolt and internal/storage/postgres.
// The interface ships complete now (be-jaavsb / be-yinl4d); a follow-up
// child of be-yinl4d may add a streaming variant if EmbeddedDoltStore
// gains a cursor-conn API. For now every Iter* method materializes the
// slice and wraps it in storage.SliceIter.
package embeddeddolt

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IterIssues streams issues matching the filter (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterIssues(ctx context.Context, query string, filter types.IssueFilter) (storage.Iter[types.Issue], error) {
	is, err := s.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(is), nil
}

// IterDependentsWithMetadata streams dependents (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterDependentsWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	deps, err := s.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(deps), nil
}

// IterDependenciesWithMetadata streams dependencies (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterDependenciesWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	deps, err := s.GetDependenciesWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(deps), nil
}

// IterIssueComments streams comments on an issue (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterIssueComments(ctx context.Context, issueID string) (storage.Iter[types.Comment], error) {
	cs, err := s.GetIssueComments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(cs), nil
}

// IterAttachments streams attachment metadata on an issue (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterAttachments(ctx context.Context, issueID string) (storage.Iter[types.Attachment], error) {
	as, err := s.ListAttachments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(as), nil
}

// IterEvents streams audit-trail events for an issue (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterEvents(ctx context.Context, issueID string, limit int) (storage.Iter[types.Event], error) {
	ev, err := s.GetEvents(ctx, issueID, limit)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ev), nil
}

// IterAllEventsSince streams every audit-trail event newer than `since`
// (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterAllEventsSince(ctx context.Context, since time.Time) (storage.Iter[types.Event], error) {
	ev, err := s.GetAllEventsSince(ctx, since)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ev), nil
}

// IterReadyWork streams ready-work issues (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterReadyWork(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.Issue], error) {
	is, err := s.GetReadyWork(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(is), nil
}

// IterBlockedIssues streams blocked issues (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterBlockedIssues(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.BlockedIssue], error) {
	bs, err := s.GetBlockedIssues(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(bs), nil
}

// IterWisps streams ephemeral issues matching the filter (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterWisps(ctx context.Context, filter types.WispFilter) (storage.Iter[types.Issue], error) {
	ws, err := s.ListWisps(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ws), nil
}

// IterAllDependencyRecords streams every dependency edge as a flat
// sequence of *types.Dependency rows (slice-then-walk).
//
// TODO(be-yinl4d-iter): replace with a fully streaming implementation.
func (s *EmbeddedDoltStore) IterAllDependencyRecords(ctx context.Context) (storage.Iter[types.Dependency], error) {
	all, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}
	var flat []*types.Dependency
	for _, deps := range all {
		flat = append(flat, deps...)
	}
	return storage.NewSliceIter(flat), nil
}
