package dolt

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestChildCounterNeverDecreases pins the invariant the reservation helper
// must preserve while it is being wired into removals and renames (GH#4750):
// child_counters is a high-water mark, so no delete, rename or create may
// lower it. Every reservation write goes through the same GREATEST upsert
// ReconcileChildCounters has always used, so a drop here would mean a
// reservation entry folded the live children instead of raising the mark.
//
// This is a guard, not a repro: the base never decreases the counter either —
// it merely leaves it behind. The test exists so the fix cannot trade the
// original bug for the opposite one.
func TestChildCounterNeverDecreases(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const parent = "ccm-parent"
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: parent, Title: parent, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	for i := 1; i <= 7; i++ {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: fmt.Sprintf("%s.%d", parent, i), Title: "child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue child %d: %v", i, err)
		}
	}

	counter := func() int {
		t.Helper()
		var last int
		if err := store.db.QueryRowContext(ctx,
			"SELECT last_child FROM child_counters WHERE parent_id = ?", parent).Scan(&last); err != nil {
			t.Fatalf("read counter: %v", err)
		}
		return last
	}
	assertNotBelow := func(step string, floor int) {
		t.Helper()
		if got := counter(); got < floor {
			t.Fatalf("after %s, counter = %d, want >= %d: the counter must never decrease (GH#4750)", step, got, floor)
		}
	}

	if got := counter(); got != 7 {
		t.Fatalf("seeded counter = %d, want 7", got)
	}

	// A low-suffix delete must not fold the counter down to the live max.
	if err := store.DeleteIssue(ctx, parent+".2"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	assertNotBelow("deleting a low child", 7)

	// A rename away must not fold it either.
	if err := store.UpdateIssueID(ctx, parent+".3", "ccm-freed", &types.Issue{
		ID: "ccm-freed", Title: "renamed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "alice"); err != nil {
		t.Fatalf("UpdateIssueID: %v", err)
	}
	assertNotBelow("renaming a child away", 7)

	// A mint must move the mark up, not reset it to the live children.
	next, err := store.GetNextChildID(ctx, parent)
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if want := parent + ".8"; next != want {
		t.Fatalf("GetNextChildID = %q, want %q", next, want)
	}
	assertNotBelow("minting a child", 8)
}
