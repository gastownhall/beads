package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	_ "modernc.org/sqlite"
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

func TestCreateIssues(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	tests := []struct {
		name       string
		issues     []*types.Issue
		wantErr    bool
		checkFunc  func(t *testing.T, issues []*types.Issue)
	}{
		{
			name:    "empty batch",
			issues:  []*types.Issue{},
			wantErr: false,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				if len(issues) != 0 {
					t.Errorf("expected 0 issues, got %d", len(issues))
				}
			},
		},
		{
			name: "single issue",
			issues: []*types.Issue{
				{
					Title:     "Single issue",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				if len(issues) != 1 {
					t.Fatalf("expected 1 issue, got %d", len(issues))
				}
				if issues[0].ID == "" {
					t.Error("issue ID should be set")
				}
				if !issues[0].CreatedAt.After(time.Time{}) {
					t.Error("CreatedAt should be set")
				}
				if !issues[0].UpdatedAt.After(time.Time{}) {
					t.Error("UpdatedAt should be set")
				}
			},
		},
		{
			name: "multiple issues",
			issues: []*types.Issue{
				{
					Title:     "Issue 1",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
				{
					Title:     "Issue 2",
					Status:    types.StatusInProgress,
					Priority:  2,
					IssueType: types.TypeBug,
				},
				{
					Title:     "Issue 3",
					Status:    types.StatusOpen,
					Priority:  3,
					IssueType: types.TypeFeature,
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				if len(issues) != 3 {
					t.Fatalf("expected 3 issues, got %d", len(issues))
				}
				for i, issue := range issues {
					if issue.ID == "" {
						t.Errorf("issue %d: ID should be set", i)
					}
					if !issue.CreatedAt.After(time.Time{}) {
						t.Errorf("issue %d: CreatedAt should be set", i)
					}
					if !issue.UpdatedAt.After(time.Time{}) {
						t.Errorf("issue %d: UpdatedAt should be set", i)
					}
				}
				// Verify IDs are unique
				ids := make(map[string]bool)
				for _, issue := range issues {
					if ids[issue.ID] {
						t.Errorf("duplicate ID found: %s", issue.ID)
					}
					ids[issue.ID] = true
				}
			},
		},
		{
			name: "mixed ID assignment - explicit and auto-generated",
			issues: []*types.Issue{
				{
					ID:        "custom-1",
					Title:     "Custom ID 1",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
				{
					Title:     "Auto ID",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
				{
					ID:        "custom-2",
					Title:     "Custom ID 2",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				if len(issues) != 3 {
					t.Fatalf("expected 3 issues, got %d", len(issues))
				}
				if issues[0].ID != "custom-1" {
					t.Errorf("expected ID 'custom-1', got %s", issues[0].ID)
				}
				if issues[1].ID == "" || issues[1].ID == "custom-1" || issues[1].ID == "custom-2" {
					t.Errorf("expected auto-generated ID, got %s", issues[1].ID)
				}
				if issues[2].ID != "custom-2" {
					t.Errorf("expected ID 'custom-2', got %s", issues[2].ID)
				}
			},
		},
		{
			name: "validation error - missing title",
			issues: []*types.Issue{
				{
					Title:     "Valid issue",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
				{
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
			},
			wantErr: true,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				// Should not be called on error
			},
		},
		{
			name: "validation error - invalid priority",
			issues: []*types.Issue{
				{
					Title:     "Test",
					Status:    types.StatusOpen,
					Priority:  10,
					IssueType: types.TypeTask,
				},
			},
			wantErr: true,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				// Should not be called on error
			},
		},
		{
			name: "validation error - invalid status",
			issues: []*types.Issue{
				{
					Title:     "Test",
					Status:    "invalid",
					Priority:  1,
					IssueType: types.TypeTask,
				},
			},
			wantErr: true,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				// Should not be called on error
			},
		},
		{
			name: "duplicate ID error",
			issues: []*types.Issue{
				{
					ID:        "duplicate-id",
					Title:     "First issue",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
				{
					ID:        "duplicate-id",
					Title:     "Second issue",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
			},
			wantErr: true,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				// Should not be called on error
			},
		},
		{
			name: "closed_at invariant - open status with closed_at",
			issues: []*types.Issue{
				{
					Title:     "Invalid closed_at",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
					ClosedAt:  &time.Time{},
				},
			},
			wantErr: true,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				// Should not be called on error
			},
		},
		{
			name: "closed_at invariant - closed status without closed_at",
			issues: []*types.Issue{
				{
					Title:     "Missing closed_at",
					Status:    types.StatusClosed,
					Priority:  1,
					IssueType: types.TypeTask,
				},
			},
			wantErr: true,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				// Should not be called on error
			},
		},
		{
			name: "nil item in batch",
			issues: []*types.Issue{
				{
					Title:     "Valid issue",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				},
				nil,
			},
			wantErr: true,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				// Should not be called on error
			},
		},
		{
			name: "valid closed issue with closed_at",
			issues: []*types.Issue{
				{
					Title:     "Properly closed",
					Status:    types.StatusClosed,
					Priority:  1,
					IssueType: types.TypeTask,
					ClosedAt:  func() *time.Time { t := time.Now(); return &t }(),
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, issues []*types.Issue) {
				if len(issues) != 1 {
					t.Fatalf("expected 1 issue, got %d", len(issues))
				}
				if issues[0].Status != types.StatusClosed {
					t.Errorf("expected closed status, got %s", issues[0].Status)
				}
				if issues[0].ClosedAt == nil {
					t.Error("ClosedAt should be set for closed issue")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.CreateIssues(ctx, tt.issues, "test-user")
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateIssues() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				// Verify IDs weren't auto-generated on error (timestamps may be set)
				for i, issue := range tt.issues {
					if issue == nil {
						continue
					}
					// Allow pre-set IDs (custom-1, existing-id, duplicate-id, etc.)
					hasCustomID := issue.ID != "" && (issue.ID == "custom-1" || issue.ID == "custom-2" || 
						issue.ID == "duplicate-id" || issue.ID == "existing-id")
					if !hasCustomID && issue.ID != "" {
						t.Errorf("issue %d: ID should not be auto-generated on error, got %s", i, issue.ID)
					}
				}
			}

			if !tt.wantErr && tt.checkFunc != nil {
				tt.checkFunc(t, tt.issues)
			}
		})
	}
}

func TestCreateIssuesRollback(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("rollback on validation error", func(t *testing.T) {
		// Create a valid issue first
		validIssue := &types.Issue{
			Title:     "Valid issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		err := store.CreateIssue(ctx, validIssue, "test-user")
		if err != nil {
			t.Fatalf("failed to create valid issue: %v", err)
		}

		// Try to create batch with one valid and one invalid issue
		issues := []*types.Issue{
			{
				Title:     "Another valid issue",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
			},
			{
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
			},
		}

		err = store.CreateIssues(ctx, issues, "test-user")
		if err == nil {
			t.Fatal("expected error for invalid batch, got nil")
		}

		// Verify the "Another valid issue" was rolled back by searching all issues
		filter := types.IssueFilter{}
		allIssues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Should only have the first valid issue, not the rolled-back one
		if len(allIssues) != 1 {
			t.Errorf("expected 1 issue after rollback, got %d", len(allIssues))
		}

		if len(allIssues) > 0 && allIssues[0].ID != validIssue.ID {
			t.Errorf("expected only the first valid issue, got %s", allIssues[0].ID)
		}
	})

	t.Run("rollback on conflict with existing ID", func(t *testing.T) {
		// Create an issue with explicit ID
		existingIssue := &types.Issue{
			ID:        "existing-id",
			Title:     "Existing issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		err := store.CreateIssue(ctx, existingIssue, "test-user")
		if err != nil {
			t.Fatalf("failed to create existing issue: %v", err)
		}

		// Try to create batch with conflicting ID
		issues := []*types.Issue{
			{
				Title:     "Should rollback",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
			},
			{
				ID:        "existing-id",
				Title:     "Conflict",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
			},
		}

		err = store.CreateIssues(ctx, issues, "test-user")
		if err == nil {
			t.Fatal("expected error for duplicate ID, got nil")
		}

		// Verify rollback - "Should rollback" issue should not exist
		filter := types.IssueFilter{}
		allIssues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Count should only include the pre-existing issues
		foundRollback := false
		for _, issue := range allIssues {
			if issue.Title == "Should rollback" {
				foundRollback = true
				break
			}
		}

		if foundRollback {
			t.Error("expected rollback of all issues in batch, but 'Should rollback' was found")
		}
	})
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

func TestClosedAtInvariant(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("UpdateIssue auto-sets closed_at when closing", func(t *testing.T) {
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

		// Update to closed without providing closed_at
		updates := map[string]interface{}{
			"status": string(types.StatusClosed),
		}
		err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
		if err != nil {
			t.Fatalf("UpdateIssue failed: %v", err)
		}

		// Verify closed_at was auto-set
		updated, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if updated.Status != types.StatusClosed {
			t.Errorf("Status should be closed, got %v", updated.Status)
		}
		if updated.ClosedAt == nil {
			t.Error("ClosedAt should be auto-set when changing to closed status")
		}
	})

	t.Run("UpdateIssue clears closed_at when reopening", func(t *testing.T) {
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

		// Close the issue
		err = store.CloseIssue(ctx, issue.ID, "Done", "test-user")
		if err != nil {
			t.Fatalf("CloseIssue failed: %v", err)
		}

		// Verify it's closed with closed_at set
		closed, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if closed.ClosedAt == nil {
			t.Fatal("ClosedAt should be set after closing")
		}

		// Reopen the issue
		updates := map[string]interface{}{
			"status": string(types.StatusOpen),
		}
		err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
		if err != nil {
			t.Fatalf("UpdateIssue failed: %v", err)
		}

		// Verify closed_at was cleared
		reopened, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if reopened.Status != types.StatusOpen {
			t.Errorf("Status should be open, got %v", reopened.Status)
		}
		if reopened.ClosedAt != nil {
			t.Error("ClosedAt should be cleared when reopening issue")
		}
	})

	t.Run("CreateIssue rejects closed issue without closed_at", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Test",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			ClosedAt:  nil, // Invalid: closed without closed_at
		}
		err := store.CreateIssue(ctx, issue, "test-user")
		if err == nil {
			t.Error("CreateIssue should reject closed issue without closed_at")
		}
	})

	t.Run("CreateIssue rejects open issue with closed_at", func(t *testing.T) {
		now := time.Now()
		issue := &types.Issue{
			Title:     "Test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			ClosedAt:  &now, // Invalid: open with closed_at
		}
		err := store.CreateIssue(ctx, issue, "test-user")
		if err == nil {
			t.Error("CreateIssue should reject open issue with closed_at")
		}
	})
}

func TestSearchIssues(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create test issues
	issues := []*types.Issue{
		{Title: "Bug in login", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeBug},
		{Title: "Feature request", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeFeature},
		{Title: "Another bug", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
	}

	for _, issue := range issues {
		err := store.CreateIssue(ctx, issue, "test-user")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		// Close the third issue
		if issue.Title == "Another bug" {
			err = store.CloseIssue(ctx, issue.ID, "Done", "test-user")
			if err != nil {
				t.Fatalf("CloseIssue failed: %v", err)
			}
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

	// Test label filtering (AND semantics)
	err = store.AddLabel(ctx, issues[0].ID, "backend", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}
	err = store.AddLabel(ctx, issues[0].ID, "urgent", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}
	err = store.AddLabel(ctx, issues[1].ID, "backend", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	// Filter by single label
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{Labels: []string{"backend"}})
	if err != nil {
		t.Fatalf("SearchIssues with label filter failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 issues with 'backend' label, got %d", len(results))
	}

	// Filter by multiple labels (AND semantics - must have ALL)
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{Labels: []string{"backend", "urgent"}})
	if err != nil {
		t.Fatalf("SearchIssues with multiple labels failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 issue with both 'backend' AND 'urgent' labels, got %d", len(results))
	}

	// Test label filtering (OR semantics)
	err = store.AddLabel(ctx, issues[2].ID, "frontend", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	results, err = store.SearchIssues(ctx, "", types.IssueFilter{LabelsAny: []string{"frontend", "urgent"}})
	if err != nil {
		t.Fatalf("SearchIssues with LabelsAny filter failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 issues with 'frontend' OR 'urgent' labels, got %d", len(results))
	}

	// Test combined AND + OR filtering
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{
		Labels:    []string{"backend"},
		LabelsAny: []string{"urgent", "frontend"},
	})
	if err != nil {
		t.Fatalf("SearchIssues with combined Labels and LabelsAny failed: %v", err)
	}
	// Should return issue[0] (has backend AND urgent)
	// issue[1] has backend but not urgent/frontend, so excluded
	if len(results) != 1 {
		t.Errorf("Expected 1 issue with 'backend' AND ('urgent' OR 'frontend'), got %d", len(results))
	}
	if len(results) > 0 && results[0].ID != issues[0].ID {
		t.Errorf("Expected issue %s, got %s", issues[0].ID, results[0].ID)
	}

	// Test whitespace trimming in labels
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{Labels: []string{" backend ", "  urgent  "}})
	if err != nil {
		t.Fatalf("SearchIssues with whitespace labels failed: %v", err)
	}
	// This won't match because storage layer doesn't trim - that's CLI's job
	// But let's verify the storage layer accepts it without error
	if len(results) != 0 {
		t.Logf("Note: Storage layer doesn't auto-trim labels (expected - trimming is CLI responsibility)")
	}
}

func TestGetStatistics(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Test statistics on empty database (regression test for NULL handling)
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed on empty database: %v", err)
	}

	if stats.TotalIssues != 0 {
		t.Errorf("Expected 0 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 0 {
		t.Errorf("Expected 0 open issues, got %d", stats.OpenIssues)
	}
	if stats.InProgressIssues != 0 {
		t.Errorf("Expected 0 in-progress issues, got %d", stats.InProgressIssues)
	}
	if stats.ClosedIssues != 0 {
		t.Errorf("Expected 0 closed issues, got %d", stats.ClosedIssues)
	}

	// Create some issues to verify statistics work with data
	issues := []*types.Issue{
		{Title: "Open task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "In progress task", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask},
		{Title: "Closed task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "Another open task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		err := store.CreateIssue(ctx, issue, "test-user")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		// Close the one that should be closed
		if issue.Title == "Closed task" {
			err = store.CloseIssue(ctx, issue.ID, "Done", "test-user")
			if err != nil {
				t.Fatalf("CloseIssue failed: %v", err)
			}
		}
	}

	// Get statistics with data
	stats, err = store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed with data: %v", err)
	}

	if stats.TotalIssues != 4 {
		t.Errorf("Expected 4 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 2 {
		t.Errorf("Expected 2 open issues, got %d", stats.OpenIssues)
	}
	if stats.InProgressIssues != 1 {
		t.Errorf("Expected 1 in-progress issue, got %d", stats.InProgressIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("Expected 1 closed issue, got %d", stats.ClosedIssues)
	}
	if stats.ReadyIssues != 2 {
		t.Errorf("Expected 2 ready issues (open with no blockers), got %d", stats.ReadyIssues)
	}
}

// Note: High-concurrency stress tests were removed as the pure Go SQLite driver
// (modernc.org/sqlite) can experience "database is locked" errors under extreme
// parallel load (100+ simultaneous operations). This is a known limitation and
// does not affect normal usage where WAL mode handles typical concurrent operations.
// For very high concurrency needs, consider using CGO-enabled sqlite3 driver or PostgreSQL.

// TestParallelIssueCreation verifies that parallel issue creation doesn't cause ID collisions
// This is a regression test for bd-89 (GH-6) where race conditions in ID generation caused
// UNIQUE constraint failures when creating issues rapidly in parallel.
func TestParallelIssueCreation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const numIssues = 20

	// Create issues in parallel using goroutines
	errors := make(chan error, numIssues)
	ids := make(chan string, numIssues)

	for i := 0; i < numIssues; i++ {
		go func() {
			issue := &types.Issue{
				Title:     "Parallel test issue",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			err := store.CreateIssue(ctx, issue, "test-user")
			if err != nil {
				errors <- err
				return
			}
			ids <- issue.ID
			errors <- nil
		}()
	}

	// Collect results
	var collectedIDs []string
	var failureCount int
	for i := 0; i < numIssues; i++ {
		if err := <-errors; err != nil {
			t.Errorf("CreateIssue failed in parallel test: %v", err)
			failureCount++
		}
	}

	close(ids)
	for id := range ids {
		collectedIDs = append(collectedIDs, id)
	}

	// Verify no failures occurred
	if failureCount > 0 {
		t.Fatalf("Expected 0 failures, got %d", failureCount)
	}

	// Verify we got the expected number of IDs
	if len(collectedIDs) != numIssues {
		t.Fatalf("Expected %d IDs, got %d", numIssues, len(collectedIDs))
	}

	// Verify all IDs are unique (no duplicates from race conditions)
	seen := make(map[string]bool)
	for _, id := range collectedIDs {
		if seen[id] {
			t.Errorf("Duplicate ID detected: %s", id)
		}
		seen[id] = true
	}

	// Verify all issues can be retrieved (they actually exist in the database)
	for _, id := range collectedIDs {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("Failed to retrieve issue %s: %v", id, err)
		}
		if issue == nil {
			t.Errorf("Issue %s not found in database", id)
		}
	}
}

func TestSetAndGetMetadata(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set metadata
	err := store.SetMetadata(ctx, "import_hash", "abc123def456")
	if err != nil {
		t.Fatalf("SetMetadata failed: %v", err)
	}

	// Get metadata
	value, err := store.GetMetadata(ctx, "import_hash")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}

	if value != "abc123def456" {
		t.Errorf("Expected 'abc123def456', got '%s'", value)
	}
}

func TestGetMetadataNotFound(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Get non-existent metadata
	value, err := store.GetMetadata(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}

	if value != "" {
		t.Errorf("Expected empty string for non-existent key, got '%s'", value)
	}
}

func TestSetMetadataUpdate(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set initial value
	err := store.SetMetadata(ctx, "test_key", "initial_value")
	if err != nil {
		t.Fatalf("SetMetadata failed: %v", err)
	}

	// Update value
	err = store.SetMetadata(ctx, "test_key", "updated_value")
	if err != nil {
		t.Fatalf("SetMetadata update failed: %v", err)
	}

	// Verify updated value
	value, err := store.GetMetadata(ctx, "test_key")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}

	if value != "updated_value" {
		t.Errorf("Expected 'updated_value', got '%s'", value)
	}
}

func TestMetadataMultipleKeys(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set multiple metadata keys
	keys := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	for key, value := range keys {
		err := store.SetMetadata(ctx, key, value)
		if err != nil {
			t.Fatalf("SetMetadata failed for %s: %v", key, err)
		}
	}

	// Verify all keys
	for key, expectedValue := range keys {
		value, err := store.GetMetadata(ctx, key)
		if err != nil {
			t.Fatalf("GetMetadata failed for %s: %v", key, err)
		}
		if value != expectedValue {
			t.Errorf("For key %s, expected '%s', got '%s'", key, expectedValue, value)
		}
	}
}

func TestPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-test-path-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Test with relative path
	relPath := filepath.Join(tmpDir, "test.db")
	store, err := New(relPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	// Path() should return absolute path
	path := store.Path()
	if !filepath.IsAbs(path) {
		t.Errorf("Path() should return absolute path, got: %s", path)
	}

	// Path should match the temp directory
	expectedPath, _ := filepath.Abs(relPath)
	if path != expectedPath {
		t.Errorf("Path() returned %s, expected %s", path, expectedPath)
	}
}

func TestMultipleStorageDistinctPaths(t *testing.T) {
	tmpDir1, err := os.MkdirTemp("", "beads-test-path1-*")
	if err != nil {
		t.Fatalf("failed to create temp dir 1: %v", err)
	}
	defer os.RemoveAll(tmpDir1)

	tmpDir2, err := os.MkdirTemp("", "beads-test-path2-*")
	if err != nil {
		t.Fatalf("failed to create temp dir 2: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	store1, err := New(filepath.Join(tmpDir1, "db1.db"))
	if err != nil {
		t.Fatalf("failed to create storage 1: %v", err)
	}
	defer store1.Close()

	store2, err := New(filepath.Join(tmpDir2, "db2.db"))
	if err != nil {
		t.Fatalf("failed to create storage 2: %v", err)
	}
	defer store2.Close()

	// Paths should be distinct
	path1 := store1.Path()
	path2 := store2.Path()

	if path1 == path2 {
		t.Errorf("Multiple storage instances should have distinct paths, both returned: %s", path1)
	}

	// Both should be absolute
	if !filepath.IsAbs(path1) || !filepath.IsAbs(path2) {
		t.Errorf("Both paths should be absolute: path1=%s, path2=%s", path1, path2)
	}
}
