package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestDeleteIssues(t *testing.T) {
	ctx := context.Background()

	t.Run("delete non-existent issue", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")
		result, err := store.DeleteIssues(ctx, []string{"bd-999"}, false, false, false)
		if err != nil {
			t.Fatalf("DeleteIssues failed: %v", err)
		}
		if result.DeletedCount != 0 {
			t.Errorf("Expected 0 deletions, got %d", result.DeletedCount)
		}
	})

	t.Run("delete with dependents - should fail without force or cascade", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		// Create issues with dependency
		issue1 := &types.Issue{ID: "bd-1", Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue2 := &types.Issue{ID: "bd-2", Title: "Child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}
		dep := &types.Dependency{IssueID: "bd-2", DependsOnID: "bd-1", Type: types.DepBlocks}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		_, err := store.DeleteIssues(ctx, []string{"bd-1"}, false, false, false)
		if err == nil {
			t.Fatal("Expected error when deleting issue with dependents")
		}
	})

	t.Run("delete with cascade - should delete all dependents", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		// Create chain: bd-1 -> bd-2 -> bd-3
		issue1 := &types.Issue{ID: "bd-1", Title: "Cascade Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue2 := &types.Issue{ID: "bd-2", Title: "Cascade Child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue3 := &types.Issue{ID: "bd-3", Title: "Cascade Grandchild", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}
		if err := store.CreateIssue(ctx, issue3, "test"); err != nil {
			t.Fatalf("Failed to create issue3: %v", err)
		}

		dep1 := &types.Dependency{IssueID: "bd-2", DependsOnID: "bd-1", Type: types.DepBlocks}
		if err := store.AddDependency(ctx, dep1, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}
		dep2 := &types.Dependency{IssueID: "bd-3", DependsOnID: "bd-2", Type: types.DepBlocks}
		if err := store.AddDependency(ctx, dep2, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		result, err := store.DeleteIssues(ctx, []string{"bd-1"}, true, false, false)
		if err != nil {
			t.Fatalf("DeleteIssues with cascade failed: %v", err)
		}
		if result.DeletedCount != 3 {
			t.Errorf("Expected 3 deletions (cascade), got %d", result.DeletedCount)
		}

		// Verify all converted to tombstones (bd-3b4)
		if issue, _ := store.GetIssue(ctx, "bd-1"); issue == nil || issue.Status != types.StatusTombstone {
			t.Error("bd-1 should be tombstone")
		}
		if issue, _ := store.GetIssue(ctx, "bd-2"); issue == nil || issue.Status != types.StatusTombstone {
			t.Error("bd-2 should be tombstone")
		}
		if issue, _ := store.GetIssue(ctx, "bd-3"); issue == nil || issue.Status != types.StatusTombstone {
			t.Error("bd-3 should be tombstone")
		}
	})

	t.Run("delete with force - should orphan dependents", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		// Create chain: bd-1 -> bd-2 -> bd-3
		issue1 := &types.Issue{ID: "bd-1", Title: "Force Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue2 := &types.Issue{ID: "bd-2", Title: "Force Child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue3 := &types.Issue{ID: "bd-3", Title: "Force Grandchild", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}
		if err := store.CreateIssue(ctx, issue3, "test"); err != nil {
			t.Fatalf("Failed to create issue3: %v", err)
		}

		dep1 := &types.Dependency{IssueID: "bd-2", DependsOnID: "bd-1", Type: types.DepBlocks}
		if err := store.AddDependency(ctx, dep1, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}
		dep2 := &types.Dependency{IssueID: "bd-3", DependsOnID: "bd-2", Type: types.DepBlocks}
		if err := store.AddDependency(ctx, dep2, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		result, err := store.DeleteIssues(ctx, []string{"bd-1"}, false, true, false)
		if err != nil {
			t.Fatalf("DeleteIssues with force failed: %v", err)
		}
		if result.DeletedCount != 1 {
			t.Errorf("Expected 1 deletion (force), got %d", result.DeletedCount)
		}
		if len(result.OrphanedIssues) != 1 || result.OrphanedIssues[0] != "bd-2" {
			t.Errorf("Expected bd-2 to be orphaned, got %v", result.OrphanedIssues)
		}

		// Verify bd-1 is tombstone, bd-2 and bd-3 still active (bd-3b4)
		if issue, _ := store.GetIssue(ctx, "bd-1"); issue == nil || issue.Status != types.StatusTombstone {
			t.Error("bd-1 should be tombstone")
		}
		if issue, _ := store.GetIssue(ctx, "bd-2"); issue == nil || issue.Status == types.StatusTombstone {
			t.Error("bd-2 should still be active")
		}
		if issue, _ := store.GetIssue(ctx, "bd-3"); issue == nil || issue.Status == types.StatusTombstone {
			t.Error("bd-3 should still be active")
		}
	})

	t.Run("dry run - should not delete", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		issue1 := &types.Issue{ID: "bd-1", Title: "DryRun Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue2 := &types.Issue{ID: "bd-2", Title: "DryRun Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}

		result, err := store.DeleteIssues(ctx, []string{"bd-1", "bd-2"}, false, true, true)
		if err != nil {
			t.Fatalf("DeleteIssues dry run failed: %v", err)
		}

		// Should report what would be deleted
		if result.DeletedCount != 2 {
			t.Errorf("Dry run should report 2 deletions, got %d", result.DeletedCount)
		}

		// But issues should still exist
		if issue, _ := store.GetIssue(ctx, "bd-1"); issue == nil {
			t.Error("bd-1 should still exist after dry run")
		}
		if issue, _ := store.GetIssue(ctx, "bd-2"); issue == nil {
			t.Error("bd-2 should still exist after dry run")
		}
	})

	t.Run("delete multiple issues at once", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		independent1 := &types.Issue{ID: "bd-10", Title: "Independent 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		independent2 := &types.Issue{ID: "bd-11", Title: "Independent 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

		if err := store.CreateIssue(ctx, independent1, "test"); err != nil {
			t.Fatalf("Failed to create independent1: %v", err)
		}
		if err := store.CreateIssue(ctx, independent2, "test"); err != nil {
			t.Fatalf("Failed to create independent2: %v", err)
		}

		result, err := store.DeleteIssues(ctx, []string{"bd-10", "bd-11"}, false, false, false)
		if err != nil {
			t.Fatalf("DeleteIssues failed: %v", err)
		}
		if result.DeletedCount != 2 {
			t.Errorf("Expected 2 deletions, got %d", result.DeletedCount)
		}

		// Verify both converted to tombstones (bd-3b4)
		if issue, _ := store.GetIssue(ctx, "bd-10"); issue == nil || issue.Status != types.StatusTombstone {
			t.Error("bd-10 should be tombstone")
		}
		if issue, _ := store.GetIssue(ctx, "bd-11"); issue == nil || issue.Status != types.StatusTombstone {
			t.Error("bd-11 should be tombstone")
		}
	})
}

func TestDeleteIssue(t *testing.T) {
	store := newTestStore(t, "file::memory:?mode=memory&cache=private")
	ctx := context.Background()

	issue := &types.Issue{
		ID:        "bd-1",
		Title:     "Single Delete Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Delete it
	if err := store.DeleteIssue(ctx, "bd-1"); err != nil {
		t.Fatalf("DeleteIssue failed: %v", err)
	}

	// Verify deleted
	if issue, _ := store.GetIssue(ctx, "bd-1"); issue != nil {
		t.Error("Issue should be deleted")
	}

	// Delete non-existent - should error
	if err := store.DeleteIssue(ctx, "bd-999"); err == nil {
		t.Error("DeleteIssue of non-existent should error")
	}
}

// TestDeleteIssueWithComments verifies that DeleteIssue also removes comments (bd-687g)
func TestDeleteIssueWithComments(t *testing.T) {
	store := newTestStore(t, "file::memory:?mode=memory&cache=private")
	ctx := context.Background()

	issue := &types.Issue{
		ID:        "bd-1",
		Title:     "Issue with Comments",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Add a comment to the comments table (not events)
	if _, err := store.AddIssueComment(ctx, "bd-1", "test-author", "This is a test comment"); err != nil {
		t.Fatalf("Failed to add comment: %v", err)
	}

	// Verify comment exists
	commentsMap, err := store.GetCommentsForIssues(ctx, []string{"bd-1"})
	if err != nil {
		t.Fatalf("Failed to get comments: %v", err)
	}
	if len(commentsMap["bd-1"]) != 1 {
		t.Fatalf("Expected 1 comment, got %d", len(commentsMap["bd-1"]))
	}

	// Delete the issue
	if err := store.DeleteIssue(ctx, "bd-1"); err != nil {
		t.Fatalf("DeleteIssue failed: %v", err)
	}

	// Verify issue deleted
	if issue, _ := store.GetIssue(ctx, "bd-1"); issue != nil {
		t.Error("Issue should be deleted")
	}

	// Verify comments also deleted (should not leak)
	commentsMap, err = store.GetCommentsForIssues(ctx, []string{"bd-1"})
	if err != nil {
		t.Fatalf("Failed to get comments after delete: %v", err)
	}
	if len(commentsMap["bd-1"]) != 0 {
		t.Errorf("Comments should be deleted, but found %d", len(commentsMap["bd-1"]))
	}
}

func TestBuildIDSet(t *testing.T) {
	ids := []string{"bd-1", "bd-2", "bd-3"}
	idSet := buildIDSet(ids)

	if len(idSet) != 3 {
		t.Errorf("Expected set size 3, got %d", len(idSet))
	}

	for _, id := range ids {
		if !idSet[id] {
			t.Errorf("ID %s should be in set", id)
		}
	}

	if idSet["bd-999"] {
		t.Error("bd-999 should not be in set")
	}
}

func TestBuildSQLInClause(t *testing.T) {
	ids := []string{"bd-1", "bd-2", "bd-3"}
	inClause, args := buildSQLInClause(ids)

	expectedClause := "?,?,?"
	if inClause != expectedClause {
		t.Errorf("Expected clause %s, got %s", expectedClause, inClause)
	}

	if len(args) != 3 {
		t.Errorf("Expected 3 args, got %d", len(args))
	}

	for i, id := range ids {
		if args[i] != id {
			t.Errorf("Args[%d]: expected %s, got %v", i, id, args[i])
		}
	}
}

// TestDeleteIssueMarksDependentsDirty verifies that when an issue is deleted,
// all issues that depend on it are marked dirty so their stale dependencies
// are removed on next JSONL export. This prevents orphan dependencies in JSONL.
func TestDeleteIssueMarksDependentsDirty(t *testing.T) {
	store := newTestStore(t, "file::memory:?mode=memory&cache=private")
	ctx := context.Background()

	// Create a wisp (will be deleted)
	wisp := &types.Issue{
		ID:        "bd-wisp-1",
		Title:     "Ephemeral Wisp",
		Status:    types.StatusClosed,
		Priority:  1,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("Failed to create wisp: %v", err)
	}

	// Create a digest that depends on the wisp
	digest := &types.Issue{
		ID:        "bd-digest-1",
		Title:     "Digest: Test",
		Status:    types.StatusClosed,
		Priority:  1,
		IssueType: types.TypeTask,
		Ephemeral: false, // Digest is persistent
	}
	if err := store.CreateIssue(ctx, digest, "test"); err != nil {
		t.Fatalf("Failed to create digest: %v", err)
	}

	// Create dependency: digest depends on wisp (parent-child)
	dep := &types.Dependency{
		IssueID:     "bd-digest-1",
		DependsOnID: "bd-wisp-1",
		Type:        types.DepParentChild,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Clear dirty state (simulate post-flush state)
	if err := store.ClearDirtyIssuesByID(ctx, []string{"bd-wisp-1", "bd-digest-1"}); err != nil {
		t.Fatalf("Failed to clear dirty state: %v", err)
	}

	// Verify digest is NOT dirty initially
	dirtyBefore, err := store.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("Failed to get dirty issues: %v", err)
	}
	for _, id := range dirtyBefore {
		if id == "bd-digest-1" {
			t.Fatal("Digest should not be dirty before wisp deletion")
		}
	}

	// Delete the wisp
	if err := store.DeleteIssue(ctx, "bd-wisp-1"); err != nil {
		t.Fatalf("Failed to delete wisp: %v", err)
	}

	// Verify digest IS now dirty (so it gets re-exported without stale dep)
	dirtyAfter, err := store.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("Failed to get dirty issues after delete: %v", err)
	}

	found := false
	for _, id := range dirtyAfter {
		if id == "bd-digest-1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Digest should be marked dirty after wisp deletion to remove orphan dependency")
	}

	// Verify the dependency is gone from the digest
	digestIssue, err := store.GetIssue(ctx, "bd-digest-1")
	if err != nil {
		t.Fatalf("Failed to get digest: %v", err)
	}
	if len(digestIssue.Dependencies) != 0 {
		t.Errorf("Digest should have no dependencies after wisp deleted, got %d", len(digestIssue.Dependencies))
	}
}
