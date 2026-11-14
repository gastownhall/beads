package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// autoMintIssue is a create that names no id.
func autoMintIssue(title string) *types.Issue {
	return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// requireIssueTitle fails t unless id reads back with title.
func requireIssueTitle(t *testing.T, ctx context.Context, store *DoltStore, id, title string) {
	t.Helper()
	got, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", id, err)
	}
	if got.Title != title {
		t.Fatalf("%s title = %q, want %q", id, got.Title, title)
	}
}

// TestCreateIssue_CounterModeJumpsPastExplicitIDs pins the counter-lag leg of
// bd-t4h0n7 (PR #6754) on the server store: explicit test-2 (issues) and
// test-100 (wisps) created after the counter's last bump hold values the
// counter would hand out next. The next auto-minted create must be test-101,
// leave both explicit rows untouched, and the one after it test-102. Before the
// fix the mint took test-2 and upserted over the explicit row.
func TestCreateIssue_CounterModeJumpsPastExplicitIDs(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig(issue_id_mode): %v", err)
	}
	first := autoMintIssue("auto one")
	if err := store.CreateIssue(ctx, first, "tester"); err != nil {
		t.Fatalf("CreateIssue(auto one): %v", err)
	}
	if first.ID != "test-1" {
		t.Fatalf("first auto id = %q, want test-1", first.ID)
	}
	explicitTwo := &types.Issue{ID: "test-2", Title: "explicit two", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, explicitTwo, "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit test-2): %v", err)
	}
	explicitHundred := &types.Issue{ID: "test-100", Title: "explicit hundred", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, explicitHundred, "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit wisp test-100): %v", err)
	}

	minted := autoMintIssue("auto two")
	if err := store.CreateIssue(ctx, minted, "tester"); err != nil {
		t.Fatalf("CreateIssue(auto two) = %v, want test-101", err)
	}
	if minted.ID != "test-101" {
		t.Errorf("auto-minted id = %q, want test-101 (one past the held test-100)", minted.ID)
	}
	requireIssueTitle(t, ctx, store, "test-2", "explicit two")
	requireIssueTitle(t, ctx, store, "test-100", "explicit hundred")

	batch := []*types.Issue{autoMintIssue("auto three")}
	if err := store.CreateIssues(ctx, batch, "tester"); err != nil {
		t.Fatalf("CreateIssues(auto three) = %v, want test-102", err)
	}
	if batch[0].ID != "test-102" {
		t.Errorf("batch auto-minted id = %q, want test-102", batch[0].ID)
	}
}
