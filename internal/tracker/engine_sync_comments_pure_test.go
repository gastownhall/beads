package tracker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

var errTestComments = errors.New("test comments error")

type commentStubStore struct {
	*pureTestStore
	comments    []*types.Comment
	commentsErr error
}

func (s *commentStubStore) GetIssueComments(_ context.Context, _ string) ([]*types.Comment, error) {
	if s.commentsErr != nil {
		return nil, s.commentsErr
	}
	return s.comments, nil
}

func (s *commentStubStore) GetConfig(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (s *commentStubStore) GetIssueByExternalRef(_ context.Context, _ string) (*types.Issue, error) {
	return nil, nil
}

func TestPullCommentsPending_ContentMembership(t *testing.T) {
	base := time.Date(2026, 2, 2, 12, 0, 0, 0, time.UTC)
	comment := func(author, text string, at time.Time) *types.Comment {
		return &types.Comment{Author: author, Text: text, CreatedAt: at}
	}

	tests := []struct {
		name            string
		local           []*types.Comment
		remote          []*types.Comment
		remoteCreatedAt time.Time
		want            bool
	}{
		{
			name:   "settled thread",
			local:  []*types.Comment{comment("alice", "first", base)},
			remote: []*types.Comment{comment("alice", "first", base)},
			want:   false,
		},
		{
			name:   "local-only comment does not mask new remote comment",
			local:  []*types.Comment{comment("alice", "first", base), comment("bob", "local note", base.Add(time.Hour))},
			remote: []*types.Comment{comment("alice", "first", base), comment("alice", "second", base.Add(2*time.Hour))},
			want:   true,
		},
		{
			name:   "edited remote text is pending",
			local:  []*types.Comment{comment("alice", "first", base)},
			remote: []*types.Comment{comment("alice", "first (edited)", base)},
			want:   true,
		},
		{
			name:   "deleted remote comment is not pending",
			local:  []*types.Comment{comment("alice", "first", base), comment("alice", "gone", base.Add(time.Hour))},
			remote: []*types.Comment{comment("alice", "first", base)},
			want:   false,
		},
		{
			name:   "empty remote thread",
			local:  []*types.Comment{comment("alice", "first", base)},
			remote: nil,
			want:   false,
		},
		{
			name:   "sub-second remote timestamp collapses onto stored second",
			local:  []*types.Comment{comment("alice", "first", base)},
			remote: []*types.Comment{comment("alice", "first", base.Add(500*time.Millisecond))},
			want:   false,
		},
		{
			name:   "same instant in another offset is settled",
			local:  []*types.Comment{comment("alice", "first", base)},
			remote: []*types.Comment{comment("alice", "first", base.In(time.FixedZone("UTC+2", 2*60*60)))},
			want:   false,
		},
		{
			name:            "zero remote timestamp falls back to issue time",
			local:           []*types.Comment{comment("alice", "first", base)},
			remote:          []*types.Comment{comment("alice", "second", time.Time{})},
			remoteCreatedAt: base,
			want:            true,
		},
		{
			name:   "zero remote and issue time is not pending",
			local:  []*types.Comment{comment("alice", "first", base)},
			remote: []*types.Comment{comment("alice", "second", time.Time{})},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &commentStubStore{pureTestStore: newPureTestStore(), comments: tt.local}
			engine := NewEngine(newMockTracker("test"), store, "test-actor")
			existing := &types.Issue{ID: "bd-x"}
			remote := &types.Issue{Comments: tt.remote, CreatedAt: tt.remoteCreatedAt}
			if got := engine.pullCommentsPending(context.Background(), existing, remote); got != tt.want {
				t.Errorf("pullCommentsPending() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPullCommentsPending_StoreErrorIsPending(t *testing.T) {
	store := &commentStubStore{
		pureTestStore: newPureTestStore(),
		commentsErr:   errTestComments,
	}
	engine := NewEngine(newMockTracker("test"), store, "test-actor")
	remote := &types.Issue{Comments: []*types.Comment{{Author: "alice", Text: "x"}}}
	if !engine.pullCommentsPending(context.Background(), &types.Issue{ID: "bd-x"}, remote) {
		t.Error("pullCommentsPending() = false on store error, want true (redundant update is harmless, a skip is not)")
	}
}

type hydratingMockTracker struct {
	*mockTracker
	withCommentsCalls int
	withCommentsIssue *TrackerIssue
	withCommentsErr   error
}

func (m *hydratingMockTracker) FetchIssueWithComments(ctx context.Context, identifier string) (*TrackerIssue, error) {
	m.withCommentsCalls++
	if m.withCommentsErr != nil {
		return nil, m.withCommentsErr
	}
	if m.withCommentsIssue != nil {
		return m.withCommentsIssue, nil
	}
	return m.mockTracker.FetchIssue(ctx, identifier)
}

func settledLocalIssue(ref string) *types.Issue {
	return &types.Issue{
		ID:          "bd-settled",
		Title:       "T",
		Description: "",
		Priority:    2,
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
	}
}

func TestSelectivePullPrefersHydratingFetch(t *testing.T) {
	ctx := context.Background()
	ref := "https://hyd.test/9"

	newEngine := func(tr IssueTracker) (*Engine, *commentStubStore) {
		store := &commentStubStore{pureTestStore: newPureTestStore(settledLocalIssue(ref))}
		return NewEngine(tr, store, "test-actor"), store
	}
	remoteIssue := func() *TrackerIssue {
		return &TrackerIssue{ID: "ext-9", Identifier: "9", URL: ref, Title: "T"}
	}

	t.Run("uses FetchIssueWithComments when implemented", func(t *testing.T) {
		inner := newMockTracker("hyd")
		inner.issues = []TrackerIssue{*remoteIssue()}
		tr := &hydratingMockTracker{mockTracker: inner, withCommentsIssue: remoteIssue()}
		engine, _ := newEngine(tr)

		result, err := engine.Sync(ctx, SyncOptions{Pull: true, IssueIDs: []string{"9"}})
		if err != nil {
			t.Fatalf("Sync() error: %v", err)
		}
		if tr.withCommentsCalls != 1 {
			t.Errorf("FetchIssueWithComments calls = %d, want 1", tr.withCommentsCalls)
		}
		if inner.fetchCalls != 0 {
			t.Errorf("plain FetchIssue calls = %d, want 0", inner.fetchCalls)
		}
		if result.Stats.Skipped != 1 {
			t.Errorf("Stats.Skipped = %d, want 1 (settled issue)", result.Stats.Skipped)
		}
	})

	t.Run("falls back to FetchIssue otherwise", func(t *testing.T) {
		tr := newMockTracker("hyd")
		tr.issues = []TrackerIssue{*remoteIssue()}
		engine, _ := newEngine(tr)

		if _, err := engine.Sync(ctx, SyncOptions{Pull: true, IssueIDs: []string{"9"}}); err != nil {
			t.Fatalf("Sync() error: %v", err)
		}
		if tr.fetchCalls != 1 {
			t.Errorf("plain FetchIssue calls = %d, want 1", tr.fetchCalls)
		}
	})
}
