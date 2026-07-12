package flatfile

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestImportIssueCommentSameCreatedAtDistinct asserts the SQL-reference
// semantics of issueops.ImportIssueCommentInTx: every call inserts a new row
// with a freshly minted unique ID, so two comments sharing a created_at
// (second-granular Dolt DATETIME exports) must both survive.
func TestImportIssueCommentSameCreatedAtDistinct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateIssue(ctx, &types.Issue{ID: "imp-1", Title: "Import target"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	c1, err := s.ImportIssueComment(ctx, "imp-1", "alice", "first", createdAt)
	if err != nil {
		t.Fatalf("ImportIssueComment #1: %v", err)
	}
	c2, err := s.ImportIssueComment(ctx, "imp-1", "alice", "second", createdAt)
	if err != nil {
		t.Fatalf("ImportIssueComment #2: %v", err)
	}
	if c1.ID == c2.ID {
		t.Errorf("minted comment IDs collide: %q", c1.ID)
	}

	comments, err := s.GetIssueComments(ctx, "imp-1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments after same-created_at import, want 2", len(comments))
	}
}

// TestBatchCreatePreservesCommentIDs asserts the SQL-reference semantics of
// issueops.PersistComments: an incoming comment's ID is preserved (imports
// must round-trip losslessly), and same-created_at comments both survive.
func TestBatchCreatePreservesCommentIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	issue := &types.Issue{
		ID:    "batch-1",
		Title: "Batch import",
		Comments: []*types.Comment{
			{ID: "orig-a", Author: "alice", Text: "first", CreatedAt: createdAt},
			{ID: "orig-b", Author: "bob", Text: "second", CreatedAt: createdAt},
		},
	}
	if err := s.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions: %v", err)
	}

	comments, err := s.GetIssueComments(ctx, "batch-1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}
	got := map[string]bool{}
	for _, c := range comments {
		got[c.ID] = true
	}
	if !got["orig-a"] || !got["orig-b"] {
		t.Errorf("original comment IDs not preserved, got %v", got)
	}
}

// TestBatchCreateCommentReimportDedup asserts the duplicate check in
// issueops.PersistComments: re-importing an issue whose comments match
// existing (author, created_at, text) rows must not duplicate them.
func TestBatchCreateCommentReimportDedup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mkIssue := func() *types.Issue {
		return &types.Issue{
			ID:    "dedup-1",
			Title: "Dedup import",
			Comments: []*types.Comment{
				{Author: "alice", Text: "hello", CreatedAt: createdAt},
			},
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.CreateIssuesWithFullOptions(ctx, []*types.Issue{mkIssue()}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("CreateIssuesWithFullOptions round %d: %v", i+1, err)
		}
	}

	comments, err := s.GetIssueComments(ctx, "dedup-1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("got %d comments after re-import, want 1 (dedup)", len(comments))
	}
}
