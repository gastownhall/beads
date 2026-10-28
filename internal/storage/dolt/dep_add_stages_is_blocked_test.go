package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestAddDependency_StagesIsBlockedFlip is the GH#6647 regression: under
// auto-commit, the is_blocked flip AddDependency writes to the source's issues
// row must ride the same Dolt commit as the edge, leaving the graph tables
// clean so a later RecomputeAllBlocked is not refused.
func TestAddDependency_StagesIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, false) // two open issues, clean committed tree
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: graph tables must be clean after seed, got dirty=%v", dirty)
	}

	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bm-w", DependsOnID: "bm-x", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatalf("precondition: bm-w must be blocked in the working set")
	}

	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("dep add must leave graph tables clean (is_blocked committed with the edge), got dirty=%v", dirty)
	}
	if !isBlockedAtHEAD(ctx, t, store, "bm-w") {
		t.Fatalf("committed HEAD has bm-w is_blocked=0 despite an open blocker")
	}
	if _, err := store.RecomputeAllBlocked(ctx); err != nil {
		t.Fatalf("RecomputeAllBlocked after dep add: %v", err)
	}
}

// TestRemoveDependency_StagesUnblockFlip is the remove-side mirror: removing
// the last blocker flips is_blocked back to 0 in the same SQL transaction as
// the edge delete, and that flip must ride the removal's Dolt commit instead
// of sitting in the working set.
func TestRemoveDependency_StagesUnblockFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true) // committed blocks edge, bm-w blocked at HEAD
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: graph tables must be clean after seed, got dirty=%v", dirty)
	}
	if !isBlockedAtHEAD(ctx, t, store, "bm-w") {
		t.Fatalf("precondition: bm-w must be blocked at HEAD before the remove")
	}

	if err := store.RemoveDependency(ctx, "bm-w", "bm-x", "tester"); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}

	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("dep remove must leave graph tables clean (unblock committed with the removal), got dirty=%v", dirty)
	}
	if isBlockedAtHEAD(ctx, t, store, "bm-w") {
		t.Fatalf("committed HEAD still has bm-w is_blocked=1 with no blocker left")
	}
}

// TestAddWispDependency_StagesDurableIsBlockedFlip is the wisp-source leg of
// GH#6647: a wisp dependency's own tables are dolt_ignored and ride the SQL
// commit, but its derived blocked-state maintenance can still flip DURABLE
// issues rows — a wisp parent that blocks darkens its durable children. Those
// flips were never Dolt-committed at all, so they sat in the working set
// forever.
func TestAddWispDependency_StagesDurableIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedIssues(ctx, t, store, "test-wispdep-w", "test-wispdep-d", "test-wispdep-a")
	// W becomes a wisp FIRST, so the parent-child edge below targets its wisp
	// row with the typed wisp target column.
	if err := store.UpdateIssue(ctx, "test-wispdep-w", map[string]interface{}{"wisp": true}, "tester"); err != nil {
		t.Fatalf("demote W to a wisp: %v", err)
	}
	// D is a durable child of the wisp parent W.
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "test-wispdep-d", DependsOnID: "test-wispdep-w", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("seed parent-child edge: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed wisp parent')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit seed: %v", err)
	}
	requireCleanTables(ctx, t, store, "issues", "dependencies")
	if isBlocked(ctx, t, store.db, "test-wispdep-d") {
		t.Fatal("precondition: an open wisp parent alone must not block its child")
	}

	// W blocks on A: the blocked parent darkens its durable child D, and that
	// flip is on the issues table — it must reach HEAD.
	if err := store.AddDependencyWithOptions(ctx, &types.Dependency{IssueID: "test-wispdep-w", DependsOnID: "test-wispdep-a", Type: types.DepBlocks}, "tester", storage.DependencyAddOptions{}); err != nil {
		t.Fatalf("AddDependencyWithOptions (wisp source): %v", err)
	}
	if !isBlocked(ctx, t, store.db, "test-wispdep-d") {
		t.Fatal("precondition: the wisp-parent blocks edge must block the durable child in the working set")
	}
	requireCleanTables(ctx, t, store, "issues")
	if !isBlockedAtHEAD(ctx, t, store, "test-wispdep-d") {
		t.Fatal("committed HEAD has test-wispdep-d is_blocked=0 under a blocked parent")
	}

	// Removing the edge unblocks the parent and with it the child — same rule.
	if err := store.RemoveDependencyWithOptions(ctx, "test-wispdep-w", "test-wispdep-a", "tester", storage.DependencyRemoveOptions{}); err != nil {
		t.Fatalf("RemoveDependencyWithOptions (wisp source): %v", err)
	}
	requireCleanTables(ctx, t, store, "issues")
	if isBlockedAtHEAD(ctx, t, store, "test-wispdep-d") {
		t.Fatal("committed HEAD still has test-wispdep-d is_blocked=1 with no blocker left")
	}
}
