package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func setupTestDB(t *testing.T) (*SQLiteStorage, func()) {
	t.Helper()

	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := New(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create storage: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func TestCreateIssue(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	issue := &types.Issue{
		Title:       "Test issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if issue.ID == "" {
		t.Error("Issue ID should be set")
	}

	if !issue.CreatedAt.After(time.Time{}) {
		t.Error("CreatedAt should be set")
	}

	if !issue.UpdatedAt.After(time.Time{}) {
		t.Error("UpdatedAt should be set")
	}
}

func TestCreateIssueValidation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	tests := []struct {
		name    string
		issue   *types.Issue
		wantErr bool
	}{
		{
			name: "valid issue",
			issue: &types.Issue{
				Title:     "Valid",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			},
			wantErr: false,
		},
		{
			name: "missing title",
			issue: &types.Issue{
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			},
			wantErr: true,
		},
		{
			name: "invalid priority",
			issue: &types.Issue{
				Title:     "Test",
				Status:    types.StatusOpen,
				Priority:  10,
				IssueType: types.TypeTask,
			},
			wantErr: true,
		},
		{
			name: "invalid status",
			issue: &types.Issue{
				Title:     "Test",
				Status:    "invalid",
				Priority:  2,
				IssueType: types.TypeTask,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.CreateIssue(ctx, tt.issue, "test-user")
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateIssue() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetIssue(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	original := &types.Issue{
		Title:              "Test issue",
		Description:        "Description",
		Design:             "Design notes",
		AcceptanceCriteria: "Acceptance",
		Notes:              "Notes",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeFeature,
		Assignee:           "alice",
	}

	err := store.CreateIssue(ctx, original, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Retrieve the issue
	retrieved, err := store.GetIssue(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetIssue returned nil")
	}

	if retrieved.ID != original.ID {
		t.Errorf("ID mismatch: got %v, want %v", retrieved.ID, original.ID)
	}

	if retrieved.Title != original.Title {
		t.Errorf("Title mismatch: got %v, want %v", retrieved.Title, original.Title)
	}

	if retrieved.Description != original.Description {
		t.Errorf("Description mismatch: got %v, want %v", retrieved.Description, original.Description)
	}

	if retrieved.Assignee != original.Assignee {
		t.Errorf("Assignee mismatch: got %v, want %v", retrieved.Assignee, original.Assignee)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	issue, err := store.GetIssue(ctx, "bd-999")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if issue != nil {
		t.Errorf("Expected nil for non-existent issue, got %v", issue)
	}
}

func TestUpdateIssue(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	issue := &types.Issue{
		Title:     "Original",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Update the issue
	updates := map[string]interface{}{
		"title":    "Updated",
		"status":   string(types.StatusInProgress),
		"priority": 1,
		"assignee": "bob",
	}

	err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
	if err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	// Verify updates
	updated, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if updated.Title != "Updated" {
		t.Errorf("Title not updated: got %v, want Updated", updated.Title)
	}

	if updated.Status != types.StatusInProgress {
		t.Errorf("Status not updated: got %v, want %v", updated.Status, types.StatusInProgress)
	}

	if updated.Priority != 1 {
		t.Errorf("Priority not updated: got %v, want 1", updated.Priority)
	}

	if updated.Assignee != "bob" {
		t.Errorf("Assignee not updated: got %v, want bob", updated.Assignee)
	}
}

func TestCloseIssue(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	issue := &types.Issue{
		Title:     "Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	err = store.CloseIssue(ctx, issue.ID, "Done", "test-user")
	if err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	// Verify closure
	closed, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if closed.Status != types.StatusClosed {
		t.Errorf("Status not closed: got %v, want %v", closed.Status, types.StatusClosed)
	}

	if closed.ClosedAt == nil {
		t.Error("ClosedAt should be set")
	}
}

func TestSearchIssues(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create test issues
	issues := []*types.Issue{
		{Title: "Bug in login", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeBug},
		{Title: "Feature request", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeFeature},
		{Title: "Another bug", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeBug},
	}

	for _, issue := range issues {
		err := store.CreateIssue(ctx, issue, "test-user")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Test query search
	results, err := store.SearchIssues(ctx, "bug", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	// Test status filter
	openStatus := types.StatusOpen
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{Status: &openStatus})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 open issues, got %d", len(results))
	}

	// Test type filter
	bugType := types.TypeBug
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{IssueType: &bugType})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 bugs, got %d", len(results))
	}

	// Test priority filter (P0)
	priority0 := 0
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{Priority: &priority0})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 P0 issue, got %d", len(results))
	}
}

func TestConcurrentIDGeneration(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const numIssues = 100

	type result struct {
		id  string
		err error
	}

	results := make(chan result, numIssues)

	// Create issues concurrently (goroutines, not processes)
	for i := 0; i < numIssues; i++ {
		go func(n int) {
			issue := &types.Issue{
				Title:     "Concurrent test",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			err := store.CreateIssue(ctx, issue, "test-user")
			results <- result{id: issue.ID, err: err}
		}(i)
	}

	// Collect results
	ids := make(map[string]bool)
	for i := 0; i < numIssues; i++ {
		res := <-results
		if res.err != nil {
			t.Errorf("CreateIssue failed: %v", res.err)
			continue
		}
		if ids[res.id] {
			t.Errorf("Duplicate ID generated: %s", res.id)
		}
		ids[res.id] = true
	}

	if len(ids) != numIssues {
		t.Errorf("Expected %d unique IDs, got %d", numIssues, len(ids))
	}
}

// TestMultiProcessIDGeneration tests ID generation across multiple processes
// This test simulates the real-world scenario of multiple `bd create` commands
// running in parallel, which is what triggers the race condition.
func TestMultiProcessIDGeneration(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "beads-multiprocess-test-*")
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
	store.Close()

	// Spawn multiple processes that each open the DB and create an issue
	const numProcesses = 20
	type result struct {
		id  string
		err error
	}

	results := make(chan result, numProcesses)

	for i := 0; i < numProcesses; i++ {
		go func(n int) {
			// Each goroutine simulates a separate process by opening a new connection
			procStore, err := New(dbPath)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer procStore.Close()

			ctx := context.Background()
			issue := &types.Issue{
				Title:     "Multi-process test",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}

			err = procStore.CreateIssue(ctx, issue, "test-user")
			results <- result{id: issue.ID, err: err}
		}(i)
	}

	// Collect results
	ids := make(map[string]bool)
	var errors []error

	for i := 0; i < numProcesses; i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
			continue
		}
		if ids[res.id] {
			t.Errorf("Duplicate ID generated: %s", res.id)
		}
		ids[res.id] = true
	}

	// With the bug, we expect UNIQUE constraint errors
	if len(errors) > 0 {
		t.Logf("Got %d errors (expected with current implementation):", len(errors))
		for _, err := range errors {
			t.Logf("  - %v", err)
		}
	}

	t.Logf("Successfully created %d unique issues out of %d attempts", len(ids), numProcesses)

	// After the fix, all should succeed
	if len(ids) != numProcesses {
		t.Errorf("Expected %d unique IDs, got %d", numProcesses, len(ids))
	}

	if len(errors) > 0 {
		t.Errorf("Expected no errors, got %d", len(errors))
	}
}
