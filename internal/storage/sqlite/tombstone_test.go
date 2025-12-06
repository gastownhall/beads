package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestCreateTombstone(t *testing.T) {
	store := newTestStore(t, "file::memory:?mode=memory&cache=private")
	ctx := context.Background()

	t.Run("create tombstone for existing issue", func(t *testing.T) {
		issue := &types.Issue{
			ID:        "bd-1",
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}

		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Create tombstone
		if err := store.CreateTombstone(ctx, "bd-1", "tester", "testing tombstone"); err != nil {
			t.Fatalf("CreateTombstone failed: %v", err)
		}

		// Verify issue still exists but is a tombstone
		tombstone, err := store.GetIssue(ctx, "bd-1")
		if err != nil {
			t.Fatalf("Failed to get tombstone: %v", err)
		}
		if tombstone == nil {
			t.Fatal("Tombstone should still exist in database")
		}
		if tombstone.Status != types.StatusTombstone {
			t.Errorf("Expected status=tombstone, got %s", tombstone.Status)
		}
		if tombstone.DeletedAt == nil {
			t.Error("DeletedAt should be set")
		}
		if tombstone.DeletedBy != "tester" {
			t.Errorf("Expected DeletedBy=tester, got %s", tombstone.DeletedBy)
		}
		if tombstone.DeleteReason != "testing tombstone" {
			t.Errorf("Expected DeleteReason='testing tombstone', got %s", tombstone.DeleteReason)
		}
		if tombstone.OriginalType != string(types.TypeTask) {
			t.Errorf("Expected OriginalType=task, got %s", tombstone.OriginalType)
		}
	})

	t.Run("create tombstone for non-existent issue", func(t *testing.T) {
		err := store.CreateTombstone(ctx, "bd-999", "tester", "testing")
		if err == nil {
			t.Error("CreateTombstone should fail for non-existent issue")
		}
	})

	t.Run("tombstone excluded from normal search", func(t *testing.T) {
		// Create two issues
		issue1 := &types.Issue{
			ID:        "bd-10",
			Title:     "Active Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		issue2 := &types.Issue{
			ID:        "bd-11",
			Title:     "To Be Deleted",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}

		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}

		// Create tombstone for issue2
		if err := store.CreateTombstone(ctx, "bd-11", "tester", "test"); err != nil {
			t.Fatalf("CreateTombstone failed: %v", err)
		}

		// Search without tombstones
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		// Should only return bd-10 (active issue)
		foundActive := false
		foundTombstone := false
		for _, issue := range results {
			if issue.ID == "bd-10" {
				foundActive = true
			}
			if issue.ID == "bd-11" {
				foundTombstone = true
			}
		}

		if !foundActive {
			t.Error("Active issue bd-10 should be in results")
		}
		if foundTombstone {
			t.Error("Tombstone bd-11 should not be in results")
		}
	})

	t.Run("tombstone included with IncludeTombstones flag", func(t *testing.T) {
		// Search with tombstones included
		filter := types.IssueFilter{IncludeTombstones: true}
		results, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		// Should return both active and tombstone
		foundActive := false
		foundTombstone := false
		for _, issue := range results {
			if issue.ID == "bd-10" {
				foundActive = true
			}
			if issue.ID == "bd-11" {
				foundTombstone = true
			}
		}

		if !foundActive {
			t.Error("Active issue bd-10 should be in results")
		}
		if !foundTombstone {
			t.Error("Tombstone bd-11 should be in results when IncludeTombstones=true")
		}
	})

	t.Run("search for tombstones explicitly", func(t *testing.T) {
		// Search for tombstone status explicitly
		status := types.StatusTombstone
		filter := types.IssueFilter{Status: &status}
		results, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		// Should only return tombstones
		for _, issue := range results {
			if issue.Status != types.StatusTombstone {
				t.Errorf("Expected only tombstones, found %s with status %s", issue.ID, issue.Status)
			}
		}

		// Should find at least bd-11
		foundTombstone := false
		for _, issue := range results {
			if issue.ID == "bd-11" {
				foundTombstone = true
			}
		}
		if !foundTombstone {
			t.Error("Should find tombstone bd-11")
		}
	})
}

func TestDeleteIssuesCreatesTombstones(t *testing.T) {
	ctx := context.Background()

	t.Run("single issue deletion creates tombstone", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		issue := &types.Issue{ID: "bd-1", Title: "Test", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeFeature}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		result, err := store.DeleteIssues(ctx, []string{"bd-1"}, false, true, false)
		if err != nil {
			t.Fatalf("DeleteIssues failed: %v", err)
		}
		if result.DeletedCount != 1 {
			t.Errorf("Expected 1 deletion, got %d", result.DeletedCount)
		}

		// Issue should still exist as tombstone
		tombstone, err := store.GetIssue(ctx, "bd-1")
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if tombstone == nil {
			t.Fatal("Issue should exist as tombstone")
		}
		if tombstone.Status != types.StatusTombstone {
			t.Errorf("Expected tombstone status, got %s", tombstone.Status)
		}
		if tombstone.OriginalType != string(types.TypeFeature) {
			t.Errorf("Expected OriginalType=feature, got %s", tombstone.OriginalType)
		}
	})

	t.Run("batch deletion creates tombstones", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		issue1 := &types.Issue{ID: "bd-10", Title: "Test 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug}
		issue2 := &types.Issue{ID: "bd-11", Title: "Test 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}

		result, err := store.DeleteIssues(ctx, []string{"bd-10", "bd-11"}, false, true, false)
		if err != nil {
			t.Fatalf("DeleteIssues failed: %v", err)
		}
		if result.DeletedCount != 2 {
			t.Errorf("Expected 2 deletions, got %d", result.DeletedCount)
		}

		// Both should exist as tombstones
		tombstone1, _ := store.GetIssue(ctx, "bd-10")
		if tombstone1 == nil || tombstone1.Status != types.StatusTombstone {
			t.Error("bd-10 should be tombstone")
		}
		if tombstone1.OriginalType != string(types.TypeBug) {
			t.Errorf("bd-10: Expected OriginalType=bug, got %s", tombstone1.OriginalType)
		}

		tombstone2, _ := store.GetIssue(ctx, "bd-11")
		if tombstone2 == nil || tombstone2.Status != types.StatusTombstone {
			t.Error("bd-11 should be tombstone")
		}
		if tombstone2.OriginalType != string(types.TypeTask) {
			t.Errorf("bd-11: Expected OriginalType=task, got %s", tombstone2.OriginalType)
		}
	})

	t.Run("cascade deletion creates tombstones", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		// Create chain: bd-1 -> bd-2 -> bd-3
		issue1 := &types.Issue{ID: "bd-1", Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
		issue2 := &types.Issue{ID: "bd-2", Title: "Child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue3 := &types.Issue{ID: "bd-3", Title: "Grandchild", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

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

		// All should exist as tombstones
		for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
			tombstone, _ := store.GetIssue(ctx, id)
			if tombstone == nil {
				t.Errorf("%s should exist as tombstone", id)
				continue
			}
			if tombstone.Status != types.StatusTombstone {
				t.Errorf("%s should have tombstone status, got %s", id, tombstone.Status)
			}
		}
	})

	t.Run("dependencies removed from tombstones", func(t *testing.T) {
		store := newTestStore(t, "file::memory:?mode=memory&cache=private")

		// Create issues with dependency
		issue1 := &types.Issue{ID: "bd-100", Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue2 := &types.Issue{ID: "bd-101", Title: "Child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}

		dep := &types.Dependency{IssueID: "bd-101", DependsOnID: "bd-100", Type: types.DepBlocks}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		// Delete parent
		_, err := store.DeleteIssues(ctx, []string{"bd-100"}, false, true, false)
		if err != nil {
			t.Fatalf("DeleteIssues failed: %v", err)
		}

		// Dependency should be removed
		deps, err := store.GetDependencies(ctx, "bd-101")
		if err != nil {
			t.Fatalf("GetDependencies failed: %v", err)
		}
		if len(deps) != 0 {
			t.Errorf("Dependency should be removed, found %d dependencies", len(deps))
		}
	})
}
