package dolt

import (
	"testing"

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
