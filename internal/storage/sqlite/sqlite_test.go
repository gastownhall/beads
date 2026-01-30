package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
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
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create storage: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx = context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Configure Gas Town custom types for test compatibility (bd-find4)
	// These types are no longer built-in but many tests use them
	if err := store.SetConfig(ctx, "types.custom", "message,merge-request,molecule,gate,agent,role,rig,convoy,event,slot"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to set types.custom: %v", err)
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

// TestCreateIssueDuplicateID verifies that CreateIssue properly rejects duplicate IDs.
// GH#956: This test ensures that insertIssueStrict (used by CreateIssue) properly fails
// on duplicate IDs instead of silently ignoring them (which would cause FK constraint
// errors when recording the creation event).
func TestCreateIssueDuplicateID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create first issue
	issue1 := &types.Issue{
		Title:     "First issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue1, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed for first issue: %v", err)
	}

	// Try to create second issue with same explicit ID - should fail
	issue2 := &types.Issue{
		ID:        issue1.ID, // Use same ID as first issue
		Title:     "Second issue with duplicate ID",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store.CreateIssue(ctx, issue2, "test-user")
	if err == nil {
		t.Error("CreateIssue should have failed for duplicate ID, but succeeded")
	}

	// Verify the error mentions constraint or duplicate
	errStr := err.Error()
	if !strings.Contains(errStr, "UNIQUE constraint") && !strings.Contains(errStr, "already exists") {
		t.Errorf("Expected error to mention constraint or duplicate, got: %v", err)
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

// createIssuesTestHelper provides test setup and assertion methods
type createIssuesTestHelper struct {
	t     *testing.T
	ctx   context.Context
	store *SQLiteStorage
}

func newCreateIssuesHelper(t *testing.T, store *SQLiteStorage) *createIssuesTestHelper {
	return &createIssuesTestHelper{t: t, ctx: context.Background(), store: store}
}

func (h *createIssuesTestHelper) newIssue(id, title string, status types.Status, priority int, issueType types.IssueType, closedAt *time.Time) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     title,
		Status:    status,
		Priority:  priority,
		IssueType: issueType,
		ClosedAt:  closedAt,
	}
}

func (h *createIssuesTestHelper) createIssues(issues []*types.Issue) error {
	return h.store.CreateIssues(h.ctx, issues, "test-user")
}

func (h *createIssuesTestHelper) assertNoError(err error) {
	if err != nil {
		h.t.Errorf("CreateIssues() unexpected error: %v", err)
	}
}

func (h *createIssuesTestHelper) assertError(err error) {
	if err == nil {
		h.t.Error("CreateIssues() expected error, got nil")
	}
}

func (h *createIssuesTestHelper) assertCount(issues []*types.Issue, expected int) {
	if len(issues) != expected {
		h.t.Errorf("expected %d issues, got %d", expected, len(issues))
	}
}

func (h *createIssuesTestHelper) assertIDSet(issue *types.Issue, index int) {
	if issue.ID == "" {
		h.t.Errorf("issue %d: ID should be set", index)
	}
}

func (h *createIssuesTestHelper) assertTimestampSet(ts time.Time, field string, index int) {
	if !ts.After(time.Time{}) {
		h.t.Errorf("issue %d: %s should be set", index, field)
	}
}

func (h *createIssuesTestHelper) assertUniqueIDs(issues []*types.Issue) {
	ids := make(map[string]bool)
	for _, issue := range issues {
		if ids[issue.ID] {
			h.t.Errorf("duplicate ID found: %s", issue.ID)
		}
		ids[issue.ID] = true
	}
}

func (h *createIssuesTestHelper) assertEqual(expected, actual interface{}, field string) {
	if expected != actual {
		h.t.Errorf("expected %s %v, got %v", field, expected, actual)
	}
}

func (h *createIssuesTestHelper) assertNotNil(value interface{}, field string) {
	if value == nil {
		h.t.Errorf("%s should be set", field)
	}
}

func (h *createIssuesTestHelper) assertNoAutoGenID(issues []*types.Issue, wantErr bool) {
	if !wantErr {
		return
	}
	for i, issue := range issues {
		if issue == nil {
			continue
		}
		hasCustomID := issue.ID != "" && (issue.ID == "bd-100" || issue.ID == "bd-200" ||
			issue.ID == "bd-999" || issue.ID == "bd-existing")
		if !hasCustomID && issue.ID != "" {
			h.t.Errorf("issue %d: ID should not be auto-generated on error, got %s", i, issue.ID)
		}
	}
}

func TestCreateIssues(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	h := newCreateIssuesHelper(t, store)

	tests := []struct {
		name      string
		issues    []*types.Issue
		wantErr   bool
		checkFunc func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue)
	}{
		{
			name:    "empty batch",
			issues:  []*types.Issue{},
			wantErr: false,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {
				h.assertCount(issues, 0)
			},
		},
		{
			name: "single issue",
			issues: []*types.Issue{
				h.newIssue("", "Single issue", types.StatusOpen, 1, types.TypeTask, nil),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {
				h.assertCount(issues, 1)
				h.assertIDSet(issues[0], 0)
				h.assertTimestampSet(issues[0].CreatedAt, "CreatedAt", 0)
				h.assertTimestampSet(issues[0].UpdatedAt, "UpdatedAt", 0)
			},
		},
		{
			name: "multiple issues",
			issues: []*types.Issue{
				h.newIssue("", "Issue 1", types.StatusOpen, 1, types.TypeTask, nil),
				h.newIssue("", "Issue 2", types.StatusInProgress, 2, types.TypeBug, nil),
				h.newIssue("", "Issue 3", types.StatusOpen, 3, types.TypeFeature, nil),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {
				h.assertCount(issues, 3)
				for i, issue := range issues {
					h.assertIDSet(issue, i)
					h.assertTimestampSet(issue.CreatedAt, "CreatedAt", i)
					h.assertTimestampSet(issue.UpdatedAt, "UpdatedAt", i)
				}
				h.assertUniqueIDs(issues)
			},
		},
		{
			name: "mixed ID assignment - explicit and auto-generated",
			issues: []*types.Issue{
				h.newIssue("bd-100", "Custom ID 1", types.StatusOpen, 1, types.TypeTask, nil),
				h.newIssue("", "Auto ID", types.StatusOpen, 1, types.TypeTask, nil),
				h.newIssue("bd-200", "Custom ID 2", types.StatusOpen, 1, types.TypeTask, nil),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {
				h.assertCount(issues, 3)
				h.assertEqual("bd-100", issues[0].ID, "ID")
				if issues[1].ID == "" || issues[1].ID == "bd-100" || issues[1].ID == "bd-200" {
					t.Errorf("expected auto-generated ID, got %s", issues[1].ID)
				}
				h.assertEqual("bd-200", issues[2].ID, "ID")
			},
		},
		{
			name: "validation error - missing title",
			issues: []*types.Issue{
				h.newIssue("", "Valid issue", types.StatusOpen, 1, types.TypeTask, nil),
				h.newIssue("", "", types.StatusOpen, 1, types.TypeTask, nil),
			},
			wantErr:   true,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {},
		},
		{
			name:      "validation error - invalid priority",
			issues:    []*types.Issue{h.newIssue("", "Test", types.StatusOpen, 10, types.TypeTask, nil)},
			wantErr:   true,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {},
		},
		{
			name:      "validation error - invalid status",
			issues:    []*types.Issue{h.newIssue("", "Test", "invalid", 1, types.TypeTask, nil)},
			wantErr:   true,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {},
		},
		{
			name: "duplicate ID error",
			issues: []*types.Issue{
				h.newIssue("bd-999", "First issue", types.StatusOpen, 1, types.TypeTask, nil),
				h.newIssue("bd-999", "Second issue", types.StatusOpen, 1, types.TypeTask, nil),
			},
			wantErr:   true,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {},
		},
		{
			name: "closed_at invariant - open status with closed_at",
			issues: []*types.Issue{
				h.newIssue("", "Invalid closed_at", types.StatusOpen, 1, types.TypeTask, &time.Time{}),
			},
			wantErr:   true,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {},
		},
		{
			name: "closed_at invariant - closed status without closed_at auto-sets it (GH#523)",
			issues: []*types.Issue{
				h.newIssue("", "Missing closed_at", types.StatusClosed, 1, types.TypeTask, nil),
			},
			wantErr: false, // Defensive fix auto-sets closed_at instead of rejecting
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {
				h.assertCount(issues, 1)
				h.assertEqual(types.StatusClosed, issues[0].Status, "status")
				if issues[0].ClosedAt == nil {
					t.Error("ClosedAt should be auto-set for closed issues (GH#523 defensive fix)")
				}
			},
		},
		{
			name: "nil item in batch",
			issues: []*types.Issue{
				h.newIssue("", "Valid issue", types.StatusOpen, 1, types.TypeTask, nil),
				nil,
			},
			wantErr:   true,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {},
		},
		{
			name: "valid closed issue with closed_at",
			issues: []*types.Issue{
				h.newIssue("", "Properly closed", types.StatusClosed, 1, types.TypeTask, func() *time.Time { t := time.Now(); return &t }()),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, h *createIssuesTestHelper, issues []*types.Issue) {
				h.assertCount(issues, 1)
				h.assertEqual(types.StatusClosed, issues[0].Status, "status")
				h.assertNotNil(issues[0].ClosedAt, "ClosedAt")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := h.createIssues(tt.issues)
			if tt.wantErr {
				h.assertError(err)
				h.assertNoAutoGenID(tt.issues, tt.wantErr)
			} else {
				h.assertNoError(err)
			}
			if !tt.wantErr && tt.checkFunc != nil {
				tt.checkFunc(t, h, tt.issues)
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

	// Note: "rollback on conflict with existing ID" test removed - CreateIssues
	// uses INSERT OR IGNORE which silently skips duplicates (needed for JSONL import)
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

func TestUpdateIssueValidation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Test invalid issue type
	updates := map[string]interface{}{
		"issue_type": "invalid-type",
	}
	err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
	if err == nil {
		t.Error("Expected error for invalid issue_type, got nil")
	}

	// Test negative estimated_minutes
	updates = map[string]interface{}{
		"estimated_minutes": -10,
	}
	err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
	if err == nil {
		t.Error("Expected error for negative estimated_minutes, got nil")
	}

	// Test valid issue type
	updates = map[string]interface{}{
		"issue_type": string(types.TypeBug),
	}
	err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
	if err != nil {
		t.Errorf("Valid issue_type should not error: %v", err)
	}

	// Test valid estimated_minutes
	updates = map[string]interface{}{
		"estimated_minutes": 60,
	}
	err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
	if err != nil {
		t.Errorf("Valid estimated_minutes should not error: %v", err)
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

	err = store.CloseIssue(ctx, issue.ID, "Done", "test-user", "")
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

	if closed.CloseReason != "Done" {
		t.Errorf("CloseReason not set: got %q, want %q", closed.CloseReason, "Done")
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
		err = store.CloseIssue(ctx, issue.ID, "Done", "test-user", "")
		if err != nil {
			t.Fatalf("CloseIssue failed: %v", err)
		}

		// Verify it's closed with closed_at and close_reason set
		closed, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if closed.ClosedAt == nil {
			t.Fatal("ClosedAt should be set after closing")
		}
		if closed.CloseReason != "Done" {
			t.Errorf("CloseReason should be 'Done', got %q", closed.CloseReason)
		}

		// Reopen the issue
		updates := map[string]interface{}{
			"status": string(types.StatusOpen),
		}
		err = store.UpdateIssue(ctx, issue.ID, updates, "test-user")
		if err != nil {
			t.Fatalf("UpdateIssue failed: %v", err)
		}

		// Verify closed_at and close_reason were cleared
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
		if reopened.CloseReason != "" {
			t.Errorf("CloseReason should be cleared when reopening issue, got %q", reopened.CloseReason)
		}
	})

	t.Run("CreateIssue auto-sets closed_at for closed issue (GH#523)", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Test",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			ClosedAt:  nil, // Defensive fix should auto-set this
		}
		err := store.CreateIssue(ctx, issue, "test-user")
		if err != nil {
			t.Errorf("CreateIssue should auto-set closed_at (GH#523 defensive fix), got error: %v", err)
		}
		if issue.ClosedAt == nil {
			t.Error("ClosedAt should be auto-set for closed issues (GH#523 defensive fix)")
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
		{Title: "Bug in login", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeBug, SpecID: "specs/auth.md"},
		{Title: "Feature request", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeFeature, SpecID: "specs/ui/flow.md"},
		{Title: "Another bug", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
	}

	for _, issue := range issues {
		err := store.CreateIssue(ctx, issue, "test-user")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		// Close the third issue
		if issue.Title == "Another bug" {
			err = store.CloseIssue(ctx, issue.ID, "Done", "test-user", "")
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

	// Test spec_id prefix filtering
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{SpecIDPrefix: "specs/"})
	if err != nil {
		t.Fatalf("SearchIssues with spec_id prefix failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 issues with spec_id prefix 'specs/', got %d", len(results))
	}
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{SpecIDPrefix: "specs/ui/"})
	if err != nil {
		t.Fatalf("SearchIssues with spec_id prefix failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 issue with spec_id prefix 'specs/ui/', got %d", len(results))
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
			err = store.CloseIssue(ctx, issue.ID, "Done", "test-user", "")
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

// TestParallelIssueCreation verifies that parallel issue creation works correctly with hash IDs
// This is a regression test for bd-89 (GH-6). With hash-based IDs, parallel creation works
// naturally since each issue gets a unique random hash - no coordination needed.
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
	ctx := context.Background()

	store, err := New(ctx, relPath)
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

	ctx := context.Background()
	store1, err := New(ctx, filepath.Join(tmpDir1, "db1.db"))
	if err != nil {
		t.Fatalf("failed to create storage 1: %v", err)
	}
	defer store1.Close()

	store2, err := New(ctx, filepath.Join(tmpDir2, "db2.db"))
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

func TestInMemoryDatabase(t *testing.T) {
	ctx := context.Background()

	// Test that :memory: database works
	ctx = context.Background()

	store, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("failed to create in-memory storage: %v", err)
	}
	defer store.Close()

	// Set issue_prefix (bd-166)
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Verify we can create and retrieve an issue
	issue := &types.Issue{
		Title:       "Test in-memory issue",
		Description: "Testing :memory: database",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	err = store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed in memory database: %v", err)
	}

	// Retrieve the issue
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed in memory database: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetIssue returned nil for in-memory issue")
	}

	if retrieved.Title != issue.Title {
		t.Errorf("Title mismatch: got %v, want %v", retrieved.Title, issue.Title)
	}

	if retrieved.Description != issue.Description {
		t.Errorf("Description mismatch: got %v, want %v", retrieved.Description, issue.Description)
	}
}

func TestInMemorySharedCache(t *testing.T) {
	t.Skip("Multiple separate New(\":memory:\") calls create independent databases - this is expected SQLite behavior")
	ctx := context.Background()

	// Create first connection
	ctx = context.Background()

	store1, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("failed to create first in-memory storage: %v", err)
	}
	defer store1.Close()

	// Set issue_prefix (bd-166)
	if err := store1.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create an issue in the first connection
	issue := &types.Issue{
		Title:       "Shared memory test",
		Description: "Testing shared cache behavior",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeBug,
	}

	err = store1.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Create second connection - Note: this creates a SEPARATE database
	// Shared cache only works within a single sql.DB connection pool
	ctx = context.Background()

	store2, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("failed to create second in-memory storage: %v", err)
	}
	defer store2.Close()

	// Retrieve the issue from the second connection
	retrieved, err := store2.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed from second connection: %v", err)
	}

	if retrieved == nil {
		t.Fatal("Shared memory cache not working: second connection can't see first connection's data")
	}

	if retrieved.Title != issue.Title {
		t.Errorf("Title mismatch: got %v, want %v", retrieved.Title, issue.Title)
	}

	// Verify both connections can see each other's changes
	issue2 := &types.Issue{
		Title:     "Second issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	err = store2.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed in second connection: %v", err)
	}

	// First connection should see the issue created by second connection
	retrieved2, err := store1.GetIssue(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetIssue failed from first connection: %v", err)
	}

	if retrieved2 == nil {
		t.Fatal("First connection can't see second connection's data")
	}

	if retrieved2.Title != issue2.Title {
		t.Errorf("Title mismatch: got %v, want %v", retrieved2.Title, issue2.Title)
	}
}

func TestGetAllConfig(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set multiple config values
	err := store.SetConfig(ctx, "key1", "value1")
	if err != nil {
		t.Fatalf("SetConfig key1 failed: %v", err)
	}

	err = store.SetConfig(ctx, "key2", "value2")
	if err != nil {
		t.Fatalf("SetConfig key2 failed: %v", err)
	}

	// Get all config
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("GetAllConfig failed: %v", err)
	}

	if len(allConfig) < 2 {
		t.Errorf("Expected at least 2 config entries, got %d", len(allConfig))
	}

	if allConfig["key1"] != "value1" {
		t.Errorf("Expected key1=value1, got %s", allConfig["key1"])
	}

	if allConfig["key2"] != "value2" {
		t.Errorf("Expected key2=value2, got %s", allConfig["key2"])
	}
}

func TestDeleteConfig(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set a config value
	err := store.SetConfig(ctx, "test-key", "test-value")
	if err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Verify it exists
	value, err := store.GetConfig(ctx, "test-key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if value != "test-value" {
		t.Errorf("Expected test-value, got %s", value)
	}

	// Delete it
	err = store.DeleteConfig(ctx, "test-key")
	if err != nil {
		t.Fatalf("DeleteConfig failed: %v", err)
	}

	// Verify it's gone
	value, err = store.GetConfig(ctx, "test-key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if value != "" {
		t.Errorf("Expected empty value after deletion, got: %s", value)
	}
}

func TestConvoyReactiveCompletion(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a convoy (using task type with gt:convoy label)
	convoy := &types.Issue{
		Title:     "Test Convoy",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask, // Use task type; gt:convoy label marks it as convoy
	}
	err := store.CreateIssue(ctx, convoy, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue convoy failed: %v", err)
	}
	if err := store.AddLabel(ctx, convoy.ID, "gt:convoy", "test-user"); err != nil {
		t.Fatalf("Failed to add gt:convoy label: %v", err)
	}

	// Create two issues to track
	issue1 := &types.Issue{
		Title:     "Tracked Issue 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store.CreateIssue(ctx, issue1, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue issue1 failed: %v", err)
	}

	issue2 := &types.Issue{
		Title:     "Tracked Issue 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue issue2 failed: %v", err)
	}

	// Add tracking dependencies: convoy tracks issue1 and issue2
	dep1 := &types.Dependency{
		IssueID:     convoy.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepTracks,
	}
	err = store.AddDependency(ctx, dep1, "test-user")
	if err != nil {
		t.Fatalf("AddDependency for issue1 failed: %v", err)
	}

	dep2 := &types.Dependency{
		IssueID:     convoy.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepTracks,
	}
	err = store.AddDependency(ctx, dep2, "test-user")
	if err != nil {
		t.Fatalf("AddDependency for issue2 failed: %v", err)
	}

	// Close first issue - convoy should still be open
	err = store.CloseIssue(ctx, issue1.ID, "Done", "test-user", "")
	if err != nil {
		t.Fatalf("CloseIssue issue1 failed: %v", err)
	}

	convoyAfter1, err := store.GetIssue(ctx, convoy.ID)
	if err != nil {
		t.Fatalf("GetIssue convoy after issue1 closed failed: %v", err)
	}
	if convoyAfter1.Status == types.StatusClosed {
		t.Error("Convoy should NOT be closed after only first tracked issue is closed")
	}

	// Close second issue - convoy should auto-close now
	err = store.CloseIssue(ctx, issue2.ID, "Done", "test-user", "")
	if err != nil {
		t.Fatalf("CloseIssue issue2 failed: %v", err)
	}

	convoyAfter2, err := store.GetIssue(ctx, convoy.ID)
	if err != nil {
		t.Fatalf("GetIssue convoy after issue2 closed failed: %v", err)
	}
	if convoyAfter2.Status != types.StatusClosed {
		t.Errorf("Convoy should be auto-closed when all tracked issues are closed, got status: %v", convoyAfter2.Status)
	}
	if convoyAfter2.CloseReason != "All tracked issues completed" {
		t.Errorf("Convoy close reason should be 'All tracked issues completed', got: %q", convoyAfter2.CloseReason)
	}
}

func TestIsClosed(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Store should not be closed initially
	if store.IsClosed() {
		t.Error("Store should not be closed initially")
	}

	// Close the store
	err := store.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Store should be closed now
	if !store.IsClosed() {
		t.Error("Store should be closed after calling Close()")
	}
}
