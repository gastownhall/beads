package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestAddLabel(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add a label
	err = store.AddLabel(ctx, issue.ID, "bug", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	// Get labels
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}

	if len(labels) != 1 {
		t.Fatalf("Expected 1 label, got %d", len(labels))
	}

	if labels[0] != "bug" {
		t.Errorf("Expected label 'bug', got '%s'", labels[0])
	}
}

func TestAddMultipleLabels(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add multiple labels
	labelsToAdd := []string{"bug", "critical", "ui"}
	for _, label := range labelsToAdd {
		err = store.AddLabel(ctx, issue.ID, label, "test-user")
		if err != nil {
			t.Fatalf("AddLabel failed for %s: %v", label, err)
		}
	}

	// Get labels
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}

	if len(labels) != 3 {
		t.Fatalf("Expected 3 labels, got %d", len(labels))
	}

	// Verify labels are sorted alphabetically
	expectedOrder := []string{"bug", "critical", "ui"}
	for i, expected := range expectedOrder {
		if labels[i] != expected {
			t.Errorf("Expected label %d to be '%s', got '%s'", i, expected, labels[i])
		}
	}
}

func TestAddDuplicateLabel(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add a label
	err = store.AddLabel(ctx, issue.ID, "bug", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	// Add the same label again - should not error (INSERT OR IGNORE)
	err = store.AddLabel(ctx, issue.ID, "bug", "test-user")
	if err != nil {
		t.Fatalf("AddLabel duplicate should not error: %v", err)
	}

	// Should still have only 1 label
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}

	if len(labels) != 1 {
		t.Errorf("Expected 1 label after duplicate add, got %d", len(labels))
	}
}

func TestRemoveLabel(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add labels
	err = store.AddLabel(ctx, issue.ID, "bug", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}
	err = store.AddLabel(ctx, issue.ID, "critical", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	// Remove one label
	err = store.RemoveLabel(ctx, issue.ID, "bug", "test-user")
	if err != nil {
		t.Fatalf("RemoveLabel failed: %v", err)
	}

	// Get labels
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}

	if len(labels) != 1 {
		t.Fatalf("Expected 1 label after removal, got %d", len(labels))
	}

	if labels[0] != "critical" {
		t.Errorf("Expected remaining label 'critical', got '%s'", labels[0])
	}
}

func TestRemoveNonexistentLabel(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Remove a label that doesn't exist - should not error
	err = store.RemoveLabel(ctx, issue.ID, "nonexistent", "test-user")
	if err != nil {
		t.Fatalf("RemoveLabel for nonexistent label should not error: %v", err)
	}
}

func TestGetLabelsEmpty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue without labels
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Get labels - should return nil or empty slice (both valid in Go)
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}

	if len(labels) != 0 {
		t.Errorf("Expected 0 labels, got %d", len(labels))
	}
}

func TestGetIssuesByLabel(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different labels
	issue1 := &types.Issue{
		Title:     "Bug 1",
		Status:    types.StatusOpen,
		Priority:  0,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue1, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	err = store.AddLabel(ctx, issue1.ID, "critical", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	issue2 := &types.Issue{
		Title:     "Bug 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err = store.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	err = store.AddLabel(ctx, issue2.ID, "critical", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	issue3 := &types.Issue{
		Title:     "Feature",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeFeature,
	}
	err = store.CreateIssue(ctx, issue3, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	err = store.AddLabel(ctx, issue3.ID, "enhancement", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	// Get issues by label "critical"
	issues, err := store.GetIssuesByLabel(ctx, "critical")
	if err != nil {
		t.Fatalf("GetIssuesByLabel failed: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("Expected 2 issues with 'critical' label, got %d", len(issues))
	}

	// Verify both critical issues are returned
	foundIssue1 := false
	foundIssue2 := false
	for _, issue := range issues {
		if issue.ID == issue1.ID {
			foundIssue1 = true
		}
		if issue.ID == issue2.ID {
			foundIssue2 = true
		}
	}

	if !foundIssue1 || !foundIssue2 {
		t.Error("Expected both critical issues to be returned")
	}
}

func TestGetIssuesByLabelEmpty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Get issues by nonexistent label
	issues, err := store.GetIssuesByLabel(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetIssuesByLabel failed: %v", err)
	}

	if len(issues) != 0 {
		t.Errorf("Expected 0 issues for nonexistent label, got %d", len(issues))
	}
}
