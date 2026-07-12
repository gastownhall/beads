package flatfile

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// SQL-reference oracle: issue rows have no comments column — incoming
// Comments are persisted only via PersistComments into the comments table,
// so GetIssue never returns a comment snapshot embedded in the row.

// rawIssueFileHasComments reads the on-disk issue JSON and reports whether a
// "comments" key was persisted.
func rawIssueFileHasComments(t *testing.T, s *FlatFileStore, id string) bool {
	t.Helper()
	data, err := os.ReadFile(s.issueFilename(id))
	if err != nil {
		t.Fatalf("read issue file %s: %v", id, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal issue file %s: %v", id, err)
	}
	_, ok := raw["comments"]
	return ok
}

func issueWithComment(id string) *types.Issue {
	return &types.Issue{
		ID:       id,
		Title:    "carries a comment",
		Priority: 2,
		Comments: []*types.Comment{{Author: "alice", Text: "imported note"}},
	}
}

func assertSingleStoredComment(t *testing.T, s *FlatFileStore, id string) {
	t.Helper()
	comments, err := s.GetIssueComments(ctx, id)
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 1 || comments[0].Text != "imported note" {
		t.Fatalf("comments store = %+v, want exactly the imported note", comments)
	}
}

func TestBatchCreateDoesNotEmbedComments(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := s.CreateIssues(ctx, []*types.Issue{issueWithComment("test-c1")}, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	if rawIssueFileHasComments(t, s, "test-c1") {
		t.Error("issue file embeds comments; they belong only in the comments store")
	}
	assertSingleStoredComment(t, s, "test-c1")
}

func TestBatchUpsertDoesNotEmbedComments(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "test-c2", Title: "original", Priority: 2}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	// Plain upsert over the existing row, incoming carries a comment.
	if err := s.CreateIssues(ctx, []*types.Issue{issueWithComment("test-c2")}, "tester"); err != nil {
		t.Fatalf("CreateIssues upsert: %v", err)
	}
	if rawIssueFileHasComments(t, s, "test-c2") {
		t.Error("upserted issue file embeds comments; they belong only in the comments store")
	}
	assertSingleStoredComment(t, s, "test-c2")
}

func TestCreateIssuePersistsCommentsToStore(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateIssue(ctx, issueWithComment("test-c3"), "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if rawIssueFileHasComments(t, s, "test-c3") {
		t.Error("issue file embeds comments; they belong only in the comments store")
	}
	assertSingleStoredComment(t, s, "test-c3")
}
