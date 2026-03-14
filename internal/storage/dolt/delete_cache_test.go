package dolt

import (
	"testing"

	"github.com/gastownhall/beads/internal/types"
)

// TestDeleteIssue_InvalidatesBlockedCache verifies that DeleteIssue clears the
// blocked-IDs cache so that subsequent bd-ready/bd-blocked calls recompute.
func TestDeleteIssue_InvalidatesBlockedCache(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create two issues with a dependency edge
	a := &types.Issue{Title: "parent", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}
	b := &types.Issue{Title: "child", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, a, "test"); err != nil {
		t.Fatalf("CreateIssue(a): %v", err)
	}
	if err := store.CreateIssue(ctx, b, "test"); err != nil {
		t.Fatalf("CreateIssue(b): %v", err)
	}
	dep := &types.Dependency{IssueID: b.ID, DependsOnID: a.ID, Type: "blocks"}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Prime the cache by computing blocked IDs
	if _, err := store.computeBlockedIDs(ctx, true); err != nil {
		t.Fatalf("computeBlockedIDs: %v", err)
	}
	if !store.blockedIDsCached {
		t.Fatal("expected blockedIDsCached=true after computeBlockedIDs")
	}

	// Delete the parent — should invalidate cache
	if err := store.DeleteIssue(ctx, a.ID); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	if store.blockedIDsCached {
		t.Error("expected blockedIDsCached=false after DeleteIssue")
	}
}

// TestDeleteIssues_InvalidatesBlockedCache verifies that batch delete clears
// the blocked-IDs cache.
func TestDeleteIssues_InvalidatesBlockedCache(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	a := &types.Issue{Title: "issue-a", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, a, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Prime the cache
	if _, err := store.computeBlockedIDs(ctx, true); err != nil {
		t.Fatalf("computeBlockedIDs: %v", err)
	}
	if !store.blockedIDsCached {
		t.Fatal("expected blockedIDsCached=true after computeBlockedIDs")
	}

	// Batch delete
	if _, err := store.DeleteIssues(ctx, []string{a.ID}, false, true, false); err != nil {
		t.Fatalf("DeleteIssues: %v", err)
	}
	if store.blockedIDsCached {
		t.Error("expected blockedIDsCached=false after DeleteIssues")
	}
}
