package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// GH#4750, removal half, on the server-backed body: every delete surface must
// raise the parent's child counter over the suffix it frees, BEFORE the row
// goes, so the next --parent mint cannot hand the freed suffix back out. Each
// test drops the parent's counter row first — the shape a database written
// before GH#4750 holds — because that is the state where a missing
// reservation recycles a suffix.

// seedChildCounterParent seeds a durable parent and children up to suffix n
// (explicit hierarchical ids), then drops the parent's counter row.
func seedChildCounterParent(t *testing.T, store *DoltStore, ctx context.Context, parent string, n int) {
	t.Helper()
	seed := func(id string) {
		t.Helper()
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	seed(parent)
	for i := 1; i <= n; i++ {
		seed(fmt.Sprintf("%s.%d", parent, i))
	}
	dropChildCounter(t, store, ctx, parent)
}

func dropChildCounter(t *testing.T, store *DoltStore, ctx context.Context, parent string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM child_counters WHERE parent_id = ?", parent); err != nil {
		t.Fatalf("drop counter for %s: %v", parent, err)
	}
}

// TestDeleteIssueReservesTheFreedChildSuffix pins the singular durable delete
// entry (issueops.DeleteIssueInTxWithResult): deleting the highest child must
// leave the parent's counter at that suffix, so the mint continues above it.
func TestDeleteIssueReservesTheFreedChildSuffix(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedChildCounterParent(t, store, ctx, "ccd-singular", 3)

	if err := store.DeleteIssue(ctx, "ccd-singular.3"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	next, err := store.GetNextChildID(ctx, "ccd-singular")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "ccd-singular.4" {
		t.Fatalf("GetNextChildID = %q, want ccd-singular.4: a deleted child's suffix must stay reserved (GH#4750)", next)
	}
}

// TestDeleteWispReservesTheFreedChildSuffix pins the wisp delete entry
// (DoltStore.deleteWisp): deleting a wisp child of a durable parent reserves
// into the DURABLE counter (routing follows the parent's plane), and the
// reservation is staged for the Dolt commit through wispDurable.
func TestDeleteWispReservesTheFreedChildSuffix(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "ccd-wparent", Title: "ccd-wparent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "ccd-wparent.3", Title: "wisp child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Ephemeral: true,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp child: %v", err)
	}
	dropChildCounter(t, store, ctx, "ccd-wparent")

	if err := store.DeleteIssue(ctx, "ccd-wparent.3"); err != nil {
		t.Fatalf("DeleteIssue(wisp): %v", err)
	}

	var last int
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", "ccd-wparent").Scan(&last); err != nil {
		t.Fatalf("read counter after wisp delete: %v", err)
	}
	if last != 3 {
		t.Fatalf("child_counters.last_child = %d, want 3: the wisp child's suffix reserves into the parent's plane (GH#4750)", last)
	}
	next, err := store.GetNextChildID(ctx, "ccd-wparent")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "ccd-wparent.4" {
		t.Fatalf("GetNextChildID = %q, want ccd-wparent.4", next)
	}
}

// TestBatchWispDeleteReservesTheFreedChildSuffix pins the batched wisp delete
// entry (DoltStore.deleteWispBatchTx, behind store.DeleteIssues): the batch's
// highest freed suffix is what the counter must cover.
func TestBatchWispDeleteReservesTheFreedChildSuffix(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "ccd-bparent", Title: "ccd-bparent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	for _, id := range []string{"ccd-bparent.3", "ccd-bparent.5"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue wisp %s: %v", id, err)
		}
	}
	dropChildCounter(t, store, ctx, "ccd-bparent")

	if _, err := store.DeleteIssues(ctx, []string{"ccd-bparent.3", "ccd-bparent.5"}, false, true, false); err != nil {
		t.Fatalf("DeleteIssues: %v", err)
	}

	next, err := store.GetNextChildID(ctx, "ccd-bparent")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "ccd-bparent.6" {
		t.Fatalf("GetNextChildID = %q, want ccd-bparent.6: the batch's highest freed suffix must stay reserved (GH#4750)", next)
	}
}

// TestCascadeDeleteDropsTheDepartingParentsChildCounter pins GH#4750's
// cascade half: migration 0039 dropped the child_counters foreign key, so a
// cascade delete that removes a parent together with its children must drop
// the parent's counter row itself — no orphaned high-water row may survive
// its owner in the versioned table.
func TestCascadeDeleteDropsTheDepartingParentsChildCounter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Parent + children with real parent-child edges, so a cascade delete
	// from the parent expands to them. The create path leaves the parent's
	// counter row at the highest live suffix (last_child = 3).
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "ccd-cascade", Title: "ccd-cascade", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	for i := 1; i <= 3; i++ {
		child := fmt.Sprintf("ccd-cascade.%d", i)
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: child, Title: child, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", child, err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: "ccd-cascade", Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency %s: %v", child, err)
		}
	}

	result, err := store.DeleteIssues(ctx, []string{"ccd-cascade"}, true, false, false)
	if err != nil {
		t.Fatalf("DeleteIssues cascade: %v", err)
	}
	if result.DeletedCount != 4 {
		t.Fatalf("DeletedCount = %d, want 4 (parent + 3 children)", result.DeletedCount)
	}

	var count int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", "ccd-cascade").Scan(&count); err != nil {
		t.Fatalf("read counter after cascade delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("child_counters rows for deleted parent = %d, want 0: a cascade delete must drop the departing parent's counter row (GH#4750)", count)
	}
}

// TestDeleteIssuesBySourceRepoReservesTheFreedChildSuffix pins the source-repo
// wipe entry (issueops.DeleteIssuesBySourceRepoInTx, behind `bd repo remove`):
// a child it removes reserves its suffix against a parent it leaves behind.
func TestDeleteIssuesBySourceRepoReservesTheFreedChildSuffix(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedChildCounterParent(t, store, ctx, "ccd-repo", 3)
	if _, err := store.db.ExecContext(ctx,
		"UPDATE issues SET source_repo = ? WHERE id = ?", "ccd-repo-x", "ccd-repo.3"); err != nil {
		t.Fatalf("tag child with source repo: %v", err)
	}

	deleted, err := store.DeleteIssuesBySourceRepo(ctx, "ccd-repo-x")
	if err != nil {
		t.Fatalf("DeleteIssuesBySourceRepo: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteIssuesBySourceRepo deleted %d, want 1", deleted)
	}

	next, err := store.GetNextChildID(ctx, "ccd-repo")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "ccd-repo.4" {
		t.Fatalf("GetNextChildID = %q, want ccd-repo.4: a wiped child's suffix must stay reserved (GH#4750)", next)
	}
}
