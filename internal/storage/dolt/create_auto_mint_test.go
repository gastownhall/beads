package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssue_CounterModeJumpsPastExplicitIDs is the server-mode twin of
// embeddeddolt's TestAutoMintedCounterIDJumpsPastExplicitIDs: explicit IDs
// created ahead of the counter must neither be overwritten nor wedge it.
func TestCreateIssue_CounterModeJumpsPastExplicitIDs(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("failed to set issue_id_mode: %v", err)
	}

	first := &types.Issue{Title: "auto first", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, first, "tester"); err != nil {
		t.Fatalf("CreateIssue(first): %v", err)
	}
	if first.ID != "test-1" {
		t.Fatalf("first counter ID = %q, want test-1", first.ID)
	}

	explicitIDs := []string{"test-2", "test-3", "test-4"}
	for _, id := range explicitIDs {
		explicit := &types.Issue{ID: id, Title: "explicit " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, explicit, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}

	for _, want := range []string{"test-5", "test-6"} {
		auto := &types.Issue{Title: "auto after lag", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, auto, "tester"); err != nil {
			t.Fatalf("auto CreateIssue (want %s): %v", want, err)
		}
		if auto.ID != want {
			t.Fatalf("auto counter ID = %q, want %s", auto.ID, want)
		}
	}

	for _, id := range explicitIDs {
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if got.Title != "explicit "+id {
			t.Fatalf("explicit %s title = %q, want %q", id, got.Title, "explicit "+id)
		}
	}
}
