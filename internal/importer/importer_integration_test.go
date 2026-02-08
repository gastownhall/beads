//go:build integration
// +build integration

package importer

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestConcurrentExternalRefUpdates tests concurrent updates to same external_ref with different timestamps
// This is a slow integration test that verifies no deadlocks occur
func TestConcurrentExternalRefUpdates(t *testing.T) {
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	externalRef := "JIRA-200"
	existing := &types.Issue{
		ID:          "bd-1",
		Title:       "Existing issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		ExternalRef: &externalRef,
	}

	if err := store.CreateIssue(ctx, existing, "test"); err != nil {
		t.Fatalf("Failed to create existing issue: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]*Result, 3)
	done := make(chan bool, 1)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			updated := &types.Issue{
				ID:          "bd-import-" + string(rune('1'+idx)),
				Title:       "Updated from worker " + string(rune('A'+idx)),
				Status:      types.StatusInProgress,
				Priority:    2,
				IssueType:   types.TypeTask,
				ExternalRef: &externalRef,
				UpdatedAt:   time.Now().Add(time.Duration(idx) * time.Second),
			}

			result, _ := ImportIssues(ctx, "", store, []*types.Issue{updated}, Options{})
			results[idx] = result
		}(i)
	}

	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		// Test completed normally
	case <-time.After(30 * time.Second):
		t.Fatal("Test timed out after 30 seconds - likely deadlock in concurrent imports")
	}

	finalIssue, err := store.GetIssueByExternalRef(ctx, externalRef)
	if err != nil {
		t.Fatalf("Failed to get final issue: %v", err)
	}

	if finalIssue == nil {
		t.Fatal("Expected final issue to exist")
	}

	// Verify that we got the update with the latest timestamp (worker 2)
	if finalIssue.Title != "Updated from worker C" {
		t.Errorf("Expected last update to win, got title: %s", finalIssue.Title)
	}
}

// TestLocalUnpushedIssueNotDeleted verifies that local issues that were never
// in git are NOT deleted during import (they are local work, not deletions)
func TestLocalUnpushedIssueNotDeleted(t *testing.T) {
	ctx := context.Background()

	// Create temp directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create a local issue that was never exported/pushed
	localIssue := &types.Issue{
		ID:        "bd-local-work",
		Title:     "Local work in progress",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}

	// Create an issue that exists in JSONL (remote)
	remoteIssue := &types.Issue{
		ID:        "bd-remote-123",
		Title:     "Synced from remote",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, remoteIssue, "test"); err != nil {
		t.Fatalf("Failed to create remote issue: %v", err)
	}

	// JSONL only contains the remote issue (local issue was never exported)
	jsonlIssues := []*types.Issue{remoteIssue}

	// Import - local issue should NOT be purged
	result, err := ImportIssues(ctx, dbPath, store, jsonlIssues, Options{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// No purges should happen (local issues not in JSONL are preserved)
	if result.Purged != 0 {
		t.Errorf("Expected 0 purged issues, got %d (purged: %v)", result.Purged, result.PurgedIDs)
	}

	// Both issues should still exist
	finalIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search final issues: %v", err)
	}

	if len(finalIssues) != 2 {
		t.Errorf("Expected 2 issues after import, got %d", len(finalIssues))
	}

	// Local work should still exist
	localFound, _ := store.GetIssue(ctx, "bd-local-work")
	if localFound == nil {
		t.Error("Local issue was incorrectly purged")
	}
}
