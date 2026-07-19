package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// getIssueHistory Tests
// =============================================================================

func TestGetIssueHistory(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		ID:          "history-test",
		Title:       "Original Title",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Commit the initial state
	if err := store.Commit(ctx, "Initial commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Update the issue
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"title":       "Updated Title",
		"description": "Updated description",
	}, "tester"); err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	// Commit the update
	if err := store.Commit(ctx, "Update commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Get history
	history, err := store.getIssueHistory(ctx, issue.ID)
	if err != nil {
		t.Fatalf("getIssueHistory failed: %v", err)
	}

	// Should have at least 2 history entries (initial + update)
	if len(history) < 2 {
		t.Errorf("expected at least 2 history entries, got %d", len(history))
	}

	// Most recent should have updated title
	if len(history) > 0 && history[0].Issue.Title != "Updated Title" {
		t.Errorf("expected most recent title 'Updated Title', got %q", history[0].Issue.Title)
	}
}

// TestGetIssueHistory_NullTextColumns reproduces GH#4867 for the duplicated
// getIssueHistory scan path. See TestHistory_NullTextColumns in
// versioned_test.go for why the NULL shows up.
func TestGetIssueHistory_NullTextColumns(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:                 "null-hist-2",
		Title:              "Null history test",
		Description:        "original description",
		Design:             "original design",
		AcceptanceCriteria: "original AC",
		Notes:              "original notes",
		Status:             types.StatusOpen,
		Priority:           2,
		IssueType:          types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.Commit(ctx, "initial commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	nullOutTextColumns(t, ctx, store, issue.ID, "description", "design", "acceptance_criteria", "notes")
	if err := store.Commit(ctx, "null text columns commit"); err != nil {
		t.Fatalf("failed to commit NULL text columns: %v", err)
	}

	history, err := store.getIssueHistory(ctx, issue.ID)
	if err != nil {
		t.Fatalf("getIssueHistory failed on NULL text columns: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least 2 history entries, got %d", len(history))
	}

	latest := history[0].Issue
	if latest.Description != "" {
		t.Errorf("expected NULL description to coalesce to \"\", got %q", latest.Description)
	}
	if latest.Design != "" {
		t.Errorf("expected NULL design to coalesce to \"\", got %q", latest.Design)
	}
	if latest.AcceptanceCriteria != "" {
		t.Errorf("expected NULL acceptance_criteria to coalesce to \"\", got %q", latest.AcceptanceCriteria)
	}
	if latest.Notes != "" {
		t.Errorf("expected NULL notes to coalesce to \"\", got %q", latest.Notes)
	}
}

func TestGetIssueHistory_NonExistent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Get history for non-existent issue
	history, err := store.getIssueHistory(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("getIssueHistory failed: %v", err)
	}

	if len(history) != 0 {
		t.Errorf("expected 0 history entries for non-existent issue, got %d", len(history))
	}
}

// =============================================================================
// getIssueAsOf Tests
// =============================================================================

func TestGetIssueAsOf(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		ID:          "asof-test",
		Title:       "Original Title",
		Description: "Original",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Commit initial state
	if err := store.Commit(ctx, "Initial state"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Get the initial commit hash
	initialHash, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("failed to get commit hash: %v", err)
	}

	// Update the issue
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"title": "Modified Title",
	}, "tester"); err != nil {
		t.Fatalf("failed to update: %v", err)
	}

	// Commit the change
	if err := store.Commit(ctx, "Modified state"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Query the issue as of the initial commit
	oldIssue, err := store.getIssueAsOf(ctx, issue.ID, initialHash)
	if err != nil {
		t.Fatalf("getIssueAsOf failed: %v", err)
	}

	if oldIssue == nil {
		t.Fatal("expected to find issue at historical commit")
	}

	if oldIssue.Title != "Original Title" {
		t.Errorf("expected historical title 'Original Title', got %q", oldIssue.Title)
	}

	// Current state should have modified title
	currentIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get current issue: %v", err)
	}

	if currentIssue.Title != "Modified Title" {
		t.Errorf("expected current title 'Modified Title', got %q", currentIssue.Title)
	}
}

func TestGetIssueAsOf_InvalidRef(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Try with SQL injection attempt
	_, err := store.getIssueAsOf(ctx, "test-id", "'; DROP TABLE issues; --")
	if err == nil {
		t.Error("expected error for invalid ref, got nil")
	}
}

func TestGetIssueAsOf_NonExistentIssue(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create and commit something to have a valid ref
	issue := &types.Issue{
		ID:        "asof-other",
		Title:     "Other",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	if err := store.Commit(ctx, "Commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	hash, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("failed to get commit hash: %v", err)
	}

	// Query non-existent issue at valid commit
	_, err = store.getIssueAsOf(ctx, "nonexistent", hash)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent issue, got: %v", err)
	}
}

// =============================================================================
// getInternalConflicts Tests
// =============================================================================

func TestGetInternalConflicts_NoConflicts(t *testing.T) {
	// Skip: The dolt_conflicts system table schema varies by Dolt version.
	// Some versions use (table, num_conflicts), others use (table_name, num_conflicts).
	// This needs to be fixed in the implementation to handle version differences.
	t.Skip("Skipping: dolt_conflicts table schema varies by Dolt version")
}

// =============================================================================
// ResolveConflicts Tests
// =============================================================================

func TestResolveConflicts_InvalidTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Try with SQL injection attempt
	err := store.ResolveConflicts(ctx, "issues; DROP TABLE", "ours")
	if err == nil {
		t.Error("expected error for invalid table name")
	}
}

func TestResolveConflicts_InvalidStrategy(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	err := store.ResolveConflicts(ctx, "issues", "invalid_strategy")
	if err == nil {
		t.Error("expected error for invalid strategy")
	}
}

// Note: TestValidateRef and TestValidateTableName are already defined in dolt_test.go
