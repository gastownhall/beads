package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCounterSyncAfterDelete verifies that counters are synced after deletion (bd-49)
func TestCounterSyncAfterDelete(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set the issue prefix to "bd" for this test
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create issues bd-1 through bd-5
	for i := 1; i <= 5; i++ {
		issue := &types.Issue{
			ID:        genID("bd", i),
			Title:     "Test issue",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Create one issue with auto-generated ID to initialize counter
	autoIssue := &types.Issue{
		Title:     "Auto issue",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, autoIssue, "test"); err != nil {
		t.Fatalf("Failed to create auto issue: %v", err)
	}
	if autoIssue.ID != "bd-6" {
		t.Fatalf("Expected auto issue to be bd-6, got %s", autoIssue.ID)
	}

	// Verify counter is at 6
	var counter int
	err := store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err != nil {
		t.Fatalf("Failed to query counter: %v", err)
	}
	if counter != 6 {
		t.Errorf("Expected counter at 6, got %d", counter)
	}

	// Delete bd-5 and bd-6
	if err := store.DeleteIssue(ctx, "bd-5"); err != nil {
		t.Fatalf("Failed to delete bd-5: %v", err)
	}
	if err := store.DeleteIssue(ctx, "bd-6"); err != nil {
		t.Fatalf("Failed to delete bd-6: %v", err)
	}

	// Counter should now be synced to 4 (max remaining ID)
	err = store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err != nil {
		t.Fatalf("Failed to query counter after delete: %v", err)
	}
	if counter != 4 {
		t.Errorf("Expected counter at 4 after deletion, got %d", counter)
	}

	// Create new issue - should be bd-5 (not bd-7)
	newIssue := &types.Issue{
		Title:     "New issue after deletion",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, newIssue, "test"); err != nil {
		t.Fatalf("Failed to create new issue: %v", err)
	}
	if newIssue.ID != "bd-5" {
		t.Errorf("Expected new issue to be bd-5, got %s", newIssue.ID)
	}
}

// TestCounterSyncAfterBatchDelete verifies that counters are synced after batch deletion (bd-49)
func TestCounterSyncAfterBatchDelete(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set the issue prefix to "bd" for this test
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create issues bd-1 through bd-10
	for i := 1; i <= 10; i++ {
		issue := &types.Issue{
			ID:        genID("bd", i),
			Title:     "Test issue",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Create one issue with auto-generated ID to initialize counter
	autoIssue := &types.Issue{
		Title:     "Auto issue",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, autoIssue, "test"); err != nil {
		t.Fatalf("Failed to create auto issue: %v", err)
	}
	if autoIssue.ID != "bd-11" {
		t.Fatalf("Expected auto issue to be bd-11, got %s", autoIssue.ID)
	}

	// Verify counter is at 11
	var counter int
	err := store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err != nil {
		t.Fatalf("Failed to query counter: %v", err)
	}
	if counter != 11 {
		t.Errorf("Expected counter at 11, got %d", counter)
	}

	// Batch delete bd-6 through bd-11
	toDelete := []string{"bd-6", "bd-7", "bd-8", "bd-9", "bd-10", "bd-11"}
	_, err = store.DeleteIssues(ctx, toDelete, false, false, false)
	if err != nil {
		t.Fatalf("Failed to batch delete: %v", err)
	}

	// Counter should now be synced to 5 (max remaining ID)
	err = store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err != nil {
		t.Fatalf("Failed to query counter after batch delete: %v", err)
	}
	if counter != 5 {
		t.Errorf("Expected counter at 5 after batch deletion, got %d", counter)
	}

	// Create new issue - should be bd-6 (not bd-11)
	newIssue := &types.Issue{
		Title:     "New issue after batch deletion",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, newIssue, "test"); err != nil {
		t.Fatalf("Failed to create new issue: %v", err)
	}
	if newIssue.ID != "bd-6" {
		t.Errorf("Expected new issue to be bd-6, got %s", newIssue.ID)
	}
}

// TestCounterSyncAfterDeleteAll verifies counter resets when all issues deleted (bd-49)
func TestCounterSyncAfterDeleteAll(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set the issue prefix to "bd" for this test
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create issues bd-1 through bd-5
	for i := 1; i <= 5; i++ {
		issue := &types.Issue{
			ID:        genID("bd", i),
			Title:     "Test issue",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Delete all issues
	toDelete := []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5"}
	_, err := store.DeleteIssues(ctx, toDelete, false, false, false)
	if err != nil {
		t.Fatalf("Failed to delete all issues: %v", err)
	}

	// Counter should be deleted (no issues left with this prefix)
	var counter int
	err = store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err == nil {
		t.Errorf("Expected no counter row, but found counter at %d", counter)
	}

	// Create new issue - should be bd-1 (fresh start)
	newIssue := &types.Issue{
		Title:     "First issue after deleting all",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, newIssue, "test"); err != nil {
		t.Fatalf("Failed to create new issue: %v", err)
	}
	if newIssue.ID != "bd-1" {
		t.Errorf("Expected new issue to be bd-1, got %s", newIssue.ID)
	}
}

// genID is a helper to generate issue IDs in tests
func genID(_ string, num int) string {
	return fmt.Sprintf("bd-%d", num)
}
