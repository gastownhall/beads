package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestLazyCounterInitialization verifies that counters are initialized lazily
// on first use, not by scanning the entire database on every CreateIssue
func TestLazyCounterInitialization(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "beads-lazy-init-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	// Initialize database
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create some issues with explicit IDs (simulating import)
	existingIssues := []string{"bd-5", "bd-10", "bd-15"}
	for _, id := range existingIssues {
		issue := &types.Issue{
			ID:        id,
			Title:     "Existing issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		err := store.CreateIssue(ctx, issue, "test-user")
		if err != nil {
			t.Fatalf("CreateIssue with explicit ID failed: %v", err)
		}
	}

	// Verify no counter exists yet (lazy init hasn't happened)
	var count int
	err = store.db.QueryRow(`SELECT COUNT(*) FROM issue_counters WHERE prefix = 'bd'`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query counters: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected no counter yet, but found %d", count)
	}

	// Now create an issue with auto-generated ID
	// This should trigger lazy initialization
	autoIssue := &types.Issue{
		Title:     "Auto-generated ID",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err = store.CreateIssue(ctx, autoIssue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue with auto ID failed: %v", err)
	}

	// Verify the ID is correct (should be bd-16, after bd-15)
	if autoIssue.ID != "bd-16" {
		t.Errorf("Expected bd-16, got %s", autoIssue.ID)
	}

	// Verify counter was initialized
	var lastID int
	err = store.db.QueryRow(`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&lastID)
	if err != nil {
		t.Fatalf("Failed to query counter: %v", err)
	}

	if lastID != 16 {
		t.Errorf("Expected counter at 16, got %d", lastID)
	}

	// Create another issue - should NOT re-scan, just increment
	anotherIssue := &types.Issue{
		Title:     "Another auto-generated ID",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err = store.CreateIssue(ctx, anotherIssue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if anotherIssue.ID != "bd-17" {
		t.Errorf("Expected bd-17, got %s", anotherIssue.ID)
	}
}

// TestLazyCounterInitializationMultiplePrefix tests lazy init with multiple prefixes
func TestLazyCounterInitializationMultiplePrefix(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set a custom prefix
	err := store.SetConfig(ctx, "issue_prefix", "custom")
	if err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Create issue with default prefix first
	err = store.SetConfig(ctx, "issue_prefix", "bd")
	if err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	bdIssue := &types.Issue{
		Title:     "BD issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err = store.CreateIssue(ctx, bdIssue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if bdIssue.ID != "bd-1" {
		t.Errorf("Expected bd-1, got %s", bdIssue.ID)
	}

	// Now switch to custom prefix
	err = store.SetConfig(ctx, "issue_prefix", "custom")
	if err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	customIssue := &types.Issue{
		Title:     "Custom issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err = store.CreateIssue(ctx, customIssue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if customIssue.ID != "custom-1" {
		t.Errorf("Expected custom-1, got %s", customIssue.ID)
	}

	// Verify both counters exist
	var count int
	err = store.db.QueryRow(`SELECT COUNT(*) FROM issue_counters`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query counters: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected 2 counters, got %d", count)
	}
}

// TestCounterInitializationFromExisting tests that the counter
// correctly initializes from the max ID of existing issues
func TestCounterInitializationFromExisting(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with explicit IDs, out of order
	explicitIDs := []string{"bd-5", "bd-100", "bd-42", "bd-7"}
	for _, id := range explicitIDs {
		issue := &types.Issue{
			ID:        id,
			Title:     "Explicit ID",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		err := store.CreateIssue(ctx, issue, "test-user")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Now auto-generate - should start at 101 (max is bd-100)
	autoIssue := &types.Issue{
		Title:     "Auto ID",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err := store.CreateIssue(ctx, autoIssue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if autoIssue.ID != "bd-101" {
		t.Errorf("Expected bd-101 (max was bd-100), got %s", autoIssue.ID)
	}
}
