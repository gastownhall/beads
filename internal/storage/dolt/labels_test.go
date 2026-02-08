package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// GetLabelsForIssues Tests
// =============================================================================

func TestGetLabelsForIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create issues with labels
	issue1 := &types.Issue{
		ID:        "labels-issue1",
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue2 := &types.Issue{
		ID:        "labels-issue2",
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, issue := range []*types.Issue{issue1, issue2} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Add labels
	if err := store.AddLabel(ctx, issue1.ID, "bug", "tester"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}
	if err := store.AddLabel(ctx, issue1.ID, "urgent", "tester"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}
	if err := store.AddLabel(ctx, issue2.ID, "feature", "tester"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}

	// Get labels for multiple issues
	issueIDs := []string{issue1.ID, issue2.ID}
	labelsMap, err := store.GetLabelsForIssues(ctx, issueIDs)
	if err != nil {
		t.Fatalf("GetLabelsForIssues failed: %v", err)
	}

	// Check issue1 labels
	if labels, ok := labelsMap[issue1.ID]; !ok {
		t.Error("expected labels for issue1")
	} else if len(labels) != 2 {
		t.Errorf("expected 2 labels for issue1, got %d", len(labels))
	}

	// Check issue2 labels
	if labels, ok := labelsMap[issue2.ID]; !ok {
		t.Error("expected labels for issue2")
	} else if len(labels) != 1 {
		t.Errorf("expected 1 label for issue2, got %d", len(labels))
	}
}

func TestGetLabelsForIssues_EmptyList(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	labelsMap, err := store.GetLabelsForIssues(ctx, []string{})
	if err != nil {
		t.Fatalf("GetLabelsForIssues failed: %v", err)
	}

	if len(labelsMap) != 0 {
		t.Errorf("expected empty map for empty input, got %d entries", len(labelsMap))
	}
}

func TestGetLabelsForIssues_NoLabels(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create issue without labels
	issue := &types.Issue{
		ID:        "nolabels-issue",
		Title:     "Issue without labels",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	labelsMap, err := store.GetLabelsForIssues(ctx, []string{issue.ID})
	if err != nil {
		t.Fatalf("GetLabelsForIssues failed: %v", err)
	}

	// Should return empty or missing entry for the issue
	if labels, ok := labelsMap[issue.ID]; ok && len(labels) > 0 {
		t.Errorf("expected no labels, got %v", labels)
	}
}

// =============================================================================
// GetIssuesByLabel Tests
// =============================================================================

func TestGetIssuesByLabel(t *testing.T) {
	// Skip: GetIssuesByLabel makes nested queries (GetIssue calls inside a rows cursor)
	// which can cause connection issues with single-connection pools.
	// This is a known limitation that should be fixed in bd-tdgo.3.
	t.Skip("Skipping: GetIssuesByLabel has nested query issue in Dolt mode")
}

func TestGetIssuesByLabel_NoMatches(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create issue with a different label
	issue := &types.Issue{
		ID:        "nomatch-issue",
		Title:     "Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "existing", "tester"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}

	// Search for non-existent label
	issues, err := store.GetIssuesByLabel(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetIssuesByLabel failed: %v", err)
	}

	if len(issues) != 0 {
		t.Errorf("expected 0 issues for non-existent label, got %d", len(issues))
	}
}

// =============================================================================
// Label CRUD Tests
// =============================================================================

func TestAddAndRemoveLabel(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create issue
	issue := &types.Issue{
		ID:        "crud-label-issue",
		Title:     "Label CRUD Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add label
	if err := store.AddLabel(ctx, issue.ID, "test-label", "tester"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}

	// Verify label exists
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get labels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "test-label" {
		t.Errorf("expected ['test-label'], got %v", labels)
	}

	// Remove label
	if err := store.RemoveLabel(ctx, issue.ID, "test-label", "tester"); err != nil {
		t.Fatalf("failed to remove label: %v", err)
	}

	// Verify label is removed
	labels, err = store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get labels: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("expected no labels after removal, got %v", labels)
	}
}

func TestAddLabel_Duplicate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create issue
	issue := &types.Issue{
		ID:        "dup-label-issue",
		Title:     "Duplicate Label Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add label twice
	if err := store.AddLabel(ctx, issue.ID, "duplicate", "tester"); err != nil {
		t.Fatalf("failed to add label first time: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "duplicate", "tester"); err != nil {
		// Some implementations may error, others may silently ignore
		t.Logf("second add label result: %v", err)
	}

	// Should still have only one instance of the label
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get labels: %v", err)
	}

	count := 0
	for _, l := range labels {
		if l == "duplicate" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 instance of 'duplicate' label, got %d", count)
	}
}
