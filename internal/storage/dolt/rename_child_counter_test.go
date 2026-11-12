package dolt

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestRenameIntoAHierarchicalIDRaisesTheParentCounter pins the rename half of
// GH#4750 on the server-backed body: renaming a top-level row X into a
// hierarchical id P.9 must raise P's child counter to cover the suffix the
// renamed row now holds. On the base the rename left the counter where the
// last --parent mint put it, so the next mint could hand out a live row's
// suffix.
func TestRenameIntoAHierarchicalIDRaisesTheParentCounter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seed := func(id string) {
		t.Helper()
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	seed("rcr-parent")
	for i := 1; i <= 3; i++ {
		seed(fmt.Sprintf("rcr-parent.%d", i))
	}
	seed("rcr-top")

	if err := store.UpdateIssueID(ctx, "rcr-top", "rcr-parent.9", &types.Issue{
		ID: "rcr-parent.9", Title: "renamed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "alice"); err != nil {
		t.Fatalf("UpdateIssueID: %v", err)
	}

	var lastChild int
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", "rcr-parent").Scan(&lastChild); err != nil {
		t.Fatalf("read counter for rcr-parent: %v", err)
	}
	if lastChild < 9 {
		t.Fatalf("child_counters.last_child for rcr-parent = %d, want >= 9 after renaming rcr-top into rcr-parent.9", lastChild)
	}
}
