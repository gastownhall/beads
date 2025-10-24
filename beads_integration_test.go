package beads_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads"
)

// TestLibraryIntegration tests the full public API that external users will use
func TestLibraryIntegration(t *testing.T) {
	// Setup: Create a temporary database
	tmpDir, err := os.MkdirTemp("", "beads-integration-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := beads.NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStorage failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Test 1: Create issue
	t.Run("CreateIssue", func(t *testing.T) {
		issue := &beads.Issue{
			Title:       "Test task",
			Description: "Integration test",
			Status:      beads.StatusOpen,
			Priority:    2,
			IssueType:   beads.TypeTask,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		err := store.CreateIssue(ctx, issue, "test-actor")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		if issue.ID == "" {
			t.Error("Issue ID should be auto-generated")
		}

		t.Logf("Created issue: %s", issue.ID)
	})

	// Test 2: Get issue
	t.Run("GetIssue", func(t *testing.T) {
		// Create an issue first
		issue := &beads.Issue{
			Title:     "Get test",
			Status:    beads.StatusOpen,
			Priority:  1,
			IssueType: beads.TypeBug,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Get it back
		retrieved, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}

		if retrieved.Title != issue.Title {
			t.Errorf("Expected title %q, got %q", issue.Title, retrieved.Title)
		}
		if retrieved.IssueType != beads.TypeBug {
			t.Errorf("Expected type bug, got %v", retrieved.IssueType)
		}
	})

	// Test 3: Update issue
	t.Run("UpdateIssue", func(t *testing.T) {
		issue := &beads.Issue{
			Title:     "Update test",
			Status:    beads.StatusOpen,
			Priority:  2,
			IssueType: beads.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Update status
		updates := map[string]interface{}{
			"status":   beads.StatusInProgress,
			"assignee": "test-user",
		}

		err := store.UpdateIssue(ctx, issue.ID, updates, "test-actor")
		if err != nil {
			t.Fatalf("UpdateIssue failed: %v", err)
		}

		// Verify update
		updated, _ := store.GetIssue(ctx, issue.ID)
		if updated.Status != beads.StatusInProgress {
			t.Errorf("Expected status in_progress, got %v", updated.Status)
		}
		if updated.Assignee != "test-user" {
			t.Errorf("Expected assignee test-user, got %q", updated.Assignee)
		}
	})

	// Test 4: Add dependency
	t.Run("AddDependency", func(t *testing.T) {
		issue1 := &beads.Issue{
			Title:     "Parent task",
			Status:    beads.StatusOpen,
			Priority:  1,
			IssueType: beads.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		issue2 := &beads.Issue{
			Title:     "Child task",
			Status:    beads.StatusOpen,
			Priority:  1,
			IssueType: beads.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		if err := store.CreateIssue(ctx, issue1, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Add dependency: issue2 blocks issue1
		dep := &beads.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        beads.DepBlocks,
			CreatedAt:   time.Now(),
			CreatedBy:   "test-actor",
		}

		err := store.AddDependency(ctx, dep, "test-actor")
		if err != nil {
			t.Fatalf("AddDependency failed: %v", err)
		}

		// Verify dependency
		deps, _ := store.GetDependencies(ctx, issue1.ID)
		if len(deps) != 1 {
			t.Fatalf("Expected 1 dependency, got %d", len(deps))
		}
		if deps[0].ID != issue2.ID {
			t.Errorf("Expected dependency on %s, got %s", issue2.ID, deps[0].ID)
		}
	})

	// Test 5: Add label
	t.Run("AddLabel", func(t *testing.T) {
		issue := &beads.Issue{
			Title:     "Label test",
			Status:    beads.StatusOpen,
			Priority:  2,
			IssueType: beads.TypeFeature,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		store.CreateIssue(ctx, issue, "test-actor")

		err := store.AddLabel(ctx, issue.ID, "urgent", "test-actor")
		if err != nil {
			t.Fatalf("AddLabel failed: %v", err)
		}

		labels, _ := store.GetLabels(ctx, issue.ID)
		if len(labels) != 1 {
			t.Fatalf("Expected 1 label, got %d", len(labels))
		}
		if labels[0] != "urgent" {
			t.Errorf("Expected label 'urgent', got %q", labels[0])
		}
	})

	// Test 6: Add comment
	t.Run("AddComment", func(t *testing.T) {
		issue := &beads.Issue{
			Title:     "Comment test",
			Status:    beads.StatusOpen,
			Priority:  2,
			IssueType: beads.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		store.CreateIssue(ctx, issue, "test-actor")

		comment, err := store.AddIssueComment(ctx, issue.ID, "test-user", "Test comment")
		if err != nil {
			t.Fatalf("AddIssueComment failed: %v", err)
		}

		if comment.Text != "Test comment" {
			t.Errorf("Expected comment text 'Test comment', got %q", comment.Text)
		}

		comments, _ := store.GetIssueComments(ctx, issue.ID)
		if len(comments) != 1 {
			t.Fatalf("Expected 1 comment, got %d", len(comments))
		}
	})

	// Test 7: Get ready work
	t.Run("GetReadyWork", func(t *testing.T) {
		// Create some issues
		for i := 0; i < 3; i++ {
			issue := &beads.Issue{
				Title:     "Ready work test",
				Status:    beads.StatusOpen,
				Priority:  i,
				IssueType: beads.TypeTask,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			store.CreateIssue(ctx, issue, "test-actor")
		}

		ready, err := store.GetReadyWork(ctx, beads.WorkFilter{
			Status: beads.StatusOpen,
			Limit:  5,
		})
		if err != nil {
			t.Fatalf("GetReadyWork failed: %v", err)
		}

		if len(ready) == 0 {
			t.Error("Expected some ready work, got none")
		}

		t.Logf("Found %d ready issues", len(ready))
	})

	// Test 8: Get statistics
	t.Run("GetStatistics", func(t *testing.T) {
		stats, err := store.GetStatistics(ctx)
		if err != nil {
			t.Fatalf("GetStatistics failed: %v", err)
		}

		if stats.TotalIssues == 0 {
			t.Error("Expected some total issues, got 0")
		}

		t.Logf("Stats: Total=%d, Open=%d, InProgress=%d, Closed=%d",
			stats.TotalIssues, stats.OpenIssues, stats.InProgressIssues, stats.ClosedIssues)
	})

	// Test 9: Close issue
	t.Run("CloseIssue", func(t *testing.T) {
		issue := &beads.Issue{
			Title:     "Close test",
			Status:    beads.StatusOpen,
			Priority:  2,
			IssueType: beads.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		store.CreateIssue(ctx, issue, "test-actor")

		err := store.CloseIssue(ctx, issue.ID, "Completed", "test-actor")
		if err != nil {
			t.Fatalf("CloseIssue failed: %v", err)
		}

		closed, _ := store.GetIssue(ctx, issue.ID)
		if closed.Status != beads.StatusClosed {
			t.Errorf("Expected status closed, got %v", closed.Status)
		}
		if closed.ClosedAt == nil {
			t.Error("Expected ClosedAt to be set")
		}
	})
}

// TestDependencyTypes ensures all dependency type constants are exported
func TestDependencyTypes(t *testing.T) {
	types := []beads.DependencyType{
		beads.DepBlocks,
		beads.DepRelated,
		beads.DepParentChild,
		beads.DepDiscoveredFrom,
	}

	for _, dt := range types {
		if dt == "" {
			t.Errorf("Dependency type should not be empty")
		}
	}
}

// TestStatusConstants ensures all status constants are exported
func TestStatusConstants(t *testing.T) {
	statuses := []beads.Status{
		beads.StatusOpen,
		beads.StatusInProgress,
		beads.StatusClosed,
		beads.StatusBlocked,
	}

	for _, s := range statuses {
		if s == "" {
			t.Errorf("Status should not be empty")
		}
	}
}

// TestIssueTypeConstants ensures all issue type constants are exported
func TestIssueTypeConstants(t *testing.T) {
	types := []beads.IssueType{
		beads.TypeBug,
		beads.TypeFeature,
		beads.TypeTask,
		beads.TypeEpic,
		beads.TypeChore,
	}

	for _, it := range types {
		if it == "" {
			t.Errorf("IssueType should not be empty")
		}
	}
}

// TestBatchCreateIssues tests creating multiple issues at once
func TestBatchCreateIssues(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-batch-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := beads.NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStorage failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create multiple issues
	issues := make([]*beads.Issue, 5)
	for i := 0; i < 5; i++ {
		issues[i] = &beads.Issue{
			Title:     "Batch test",
			Status:    beads.StatusOpen,
			Priority:  2,
			IssueType: beads.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	}

	err = store.CreateIssues(ctx, issues, "test-actor")
	if err != nil {
		t.Fatalf("CreateIssues failed: %v", err)
	}

	// Verify all got IDs
	for i, issue := range issues {
		if issue.ID == "" {
			t.Errorf("Issue %d should have ID set", i)
		}
	}
}

// TestFindDatabasePathIntegration tests the database discovery
func TestFindDatabasePathIntegration(t *testing.T) {
	// Save original working directory
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create temporary directory with .beads
	tmpDir, err := os.MkdirTemp("", "beads-find-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0o755)

	dbPath := filepath.Join(beadsDir, "test.db")
	f, _ := os.Create(dbPath)
	f.Close()

	// Change to temp directory
	os.Chdir(tmpDir)

	// Should find the database
	found := beads.FindDatabasePath()
	if found == "" {
		t.Error("Expected to find database, got empty string")
	}

	t.Logf("Found database at: %s", found)
}

// TestRoundTripIssue tests creating, updating, and retrieving an issue
func TestRoundTripIssue(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-roundtrip-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := beads.NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStorage failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create issue with all fields
	original := &beads.Issue{
		Title:              "Complete issue",
		Description:        "Full description",
		Design:             "Design notes",
		AcceptanceCriteria: "Acceptance criteria",
		Notes:              "Implementation notes",
		Status:             beads.StatusOpen,
		Priority:           1,
		IssueType:          beads.TypeFeature,
		Assignee:           "developer",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	err = store.CreateIssue(ctx, original, "test-actor")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Retrieve and verify all fields
	retrieved, err := store.GetIssue(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if retrieved.Title != original.Title {
		t.Errorf("Title mismatch: expected %q, got %q", original.Title, retrieved.Title)
	}
	if retrieved.Description != original.Description {
		t.Errorf("Description mismatch")
	}
	if retrieved.Design != original.Design {
		t.Errorf("Design mismatch")
	}
	if retrieved.AcceptanceCriteria != original.AcceptanceCriteria {
		t.Errorf("AcceptanceCriteria mismatch")
	}
	if retrieved.Notes != original.Notes {
		t.Errorf("Notes mismatch")
	}
	if retrieved.Status != original.Status {
		t.Errorf("Status mismatch")
	}
	if retrieved.Priority != original.Priority {
		t.Errorf("Priority mismatch")
	}
	if retrieved.IssueType != original.IssueType {
		t.Errorf("IssueType mismatch")
	}
	if retrieved.Assignee != original.Assignee {
		t.Errorf("Assignee mismatch")
	}
}
