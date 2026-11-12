//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDeleteIssueReservesTheFreedChildSuffix is the embedded-store twin of the
// server-backed singular-delete test (internal/storage/dolt): deleting the
// highest child must leave the parent's counter at that suffix, so the next
// --parent mint continues above it (GH#4750). The counter row is dropped by
// hand first — the shape a database written before GH#4750 holds.
func TestDeleteIssueReservesTheFreedChildSuffix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cd")
	ctx := t.Context()

	seed := func(id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	seed("cd-parent")
	for i := 1; i <= 3; i++ {
		seed(fmt.Sprintf("cd-parent.%d", i))
	}
	te.exec(t, ctx, "DELETE FROM child_counters WHERE parent_id = ?", "cd-parent")

	if err := te.store.DeleteIssue(ctx, "cd-parent.3"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	next, err := te.store.GetNextChildID(ctx, "cd-parent")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "cd-parent.4" {
		t.Fatalf("GetNextChildID = %q, want cd-parent.4: a deleted child's suffix must stay reserved (GH#4750)", next)
	}
}

// TestCascadeDeleteDropsTheDepartingParentsChildCounter is the embedded twin
// of the server-backed cascade test (internal/storage/dolt): a cascade delete
// that removes a parent together with its children must leave no counter row
// owned by the deleted parent (GH#4750, AC-7). The embedded engine does not
// enforce the child_counters parent foreign key, so the delete path itself
// must drop the departing parent's row.
func TestCascadeDeleteDropsTheDepartingParentsChildCounter(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cd")
	ctx := t.Context()

	// Parent + children with real parent-child edges, so a cascade delete
	// from the parent expands to them. The create path leaves the parent's
	// counter row at the highest live suffix (last_child = 3).
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: "cd-cascade", Title: "cd-cascade", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	for i := 1; i <= 3; i++ {
		child := fmt.Sprintf("cd-cascade.%d", i)
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: child, Title: child, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", child, err)
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: "cd-cascade", Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency %s: %v", child, err)
		}
	}

	result, err := te.store.DeleteIssues(ctx, []string{"cd-cascade"}, true, false, false)
	if err != nil {
		t.Fatalf("DeleteIssues cascade: %v", err)
	}
	if result.DeletedCount != 4 {
		t.Fatalf("DeletedCount = %d, want 4 (parent + 3 children)", result.DeletedCount)
	}

	te.assertNoChildCounterRow(t, ctx, "child_counters", "cd-cascade")
}
