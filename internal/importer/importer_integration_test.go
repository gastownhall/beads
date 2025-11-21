//go:build integration
// +build integration

package importer

import (
	"context"
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
