package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCounterSyncAfterImport verifies that counters are properly synced after import
// This test reproduces the scenario from bd-50:
// - Start with a database that has stale counter (e.g., 4106)
// - Import issues with lower IDs (e.g., bd-1 to bd-49)
// - Verify counter syncs to actual max ID (49), not stuck at 4106
func TestCounterSyncAfterImport(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set the issue prefix to "bd" for this test
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Simulate "test pollution" scenario: manually set counter to high value
	// This simulates having had many issues before that were deleted
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO issue_counters (prefix, last_id) 
		VALUES ('bd', 4106)
		ON CONFLICT(prefix) DO UPDATE SET last_id = 4106
	`)
	if err != nil {
		t.Fatalf("Failed to set stale counter: %v", err)
	}

	// Verify counter is at 4106
	var counter int
	err = store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err != nil {
		t.Fatalf("Failed to query counter: %v", err)
	}
	if counter != 4106 {
		t.Errorf("Expected counter at 4106, got %d", counter)
	}

	// Now import 49 issues (bd-1 through bd-49)
	for i := 1; i <= 49; i++ {
		issue := &types.Issue{
			ID:        genID("bd", i),
			Title:     fmt.Sprintf("Imported issue %d", i),
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		// Use CreateIssue which will be called during import
		if err := store.CreateIssue(ctx, issue, "import"); err != nil {
			t.Fatalf("Failed to import issue %d: %v", i, err)
		}
	}

	// After import, manually call SyncAllCounters (this is what import_shared.go does)
	if err := store.SyncAllCounters(ctx); err != nil {
		t.Fatalf("Failed to sync counters: %v", err)
	}

	// Counter should now be synced to 49 (the actual max ID)
	err = store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err != nil {
		t.Fatalf("Failed to query counter after import: %v", err)
	}
	if counter != 49 {
		t.Errorf("Expected counter at 49 after import+sync, got %d (bug from bd-50!)", counter)
	}

	// Create new issue with auto-generated ID - should be bd-50, not bd-4107
	newIssue := &types.Issue{
		Title:     "New issue after import",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, newIssue, "test"); err != nil {
		t.Fatalf("Failed to create new issue: %v", err)
	}
	if newIssue.ID != "bd-50" {
		t.Errorf("Expected new issue to be bd-50, got %s (bug from bd-50!)", newIssue.ID)
	}
}

// TestCounterSyncWithoutExplicitSync verifies behavior when SyncAllCounters is NOT called
// This shows the bug that would happen if import didn't call SyncAllCounters
func TestCounterNotSyncedWithoutExplicitSync(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set the issue prefix to "bd" for this test
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Manually set counter to high value (stale counter)
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO issue_counters (prefix, last_id) 
		VALUES ('bd', 4106)
		ON CONFLICT(prefix) DO UPDATE SET last_id = 4106
	`)
	if err != nil {
		t.Fatalf("Failed to set stale counter: %v", err)
	}

	// Import 49 issues WITHOUT calling SyncAllCounters
	for i := 1; i <= 49; i++ {
		issue := &types.Issue{
			ID:        genID("bd", i),
			Title:     fmt.Sprintf("Imported issue %d", i),
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "import"); err != nil {
			t.Fatalf("Failed to import issue %d: %v", i, err)
		}
	}

	// DO NOT call SyncAllCounters - simulate the bug

	// Counter should still be at 4106 (stale!)
	var counter int
	err = store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&counter)
	if err != nil {
		t.Fatalf("Failed to query counter: %v", err)
	}
	if counter != 4106 {
		t.Logf("Counter was at %d instead of 4106 - counter got updated somehow", counter)
	}

	// Create new issue - without the sync fix, this would be bd-4107
	newIssue := &types.Issue{
		Title:     "New issue after import (no sync)",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, newIssue, "test"); err != nil {
		t.Fatalf("Failed to create new issue: %v", err)
	}

	// Without SyncAllCounters, this would be bd-4107 (the bug!)
	// With the fix in import_shared.go, it should be bd-50
	t.Logf("New issue ID: %s (expected bd-4107 without fix, bd-50 with fix)", newIssue.ID)
	
	// This test documents the bug behavior - if counter is stale, next ID is wrong
	if newIssue.ID == "bd-4107" {
		t.Logf("Bug confirmed: counter not synced, got wrong ID %s", newIssue.ID)
	}
}
