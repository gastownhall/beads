package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestBatchGetLabelsAndComments verifies that GetLabelsForIssues and GetCommentsForIssues
// correctly fetch data for multiple issues in a single query (avoiding N+1 pattern)
func TestBatchGetLabelsAndComments(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create test issues
	issues := []*types.Issue{
		{
			ID:        "bd-batch1",
			Title:     "Issue 1",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  1,
		},
		{
			ID:        "bd-batch2",
			Title:     "Issue 2",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  1,
		},
		{
			ID:        "bd-batch3",
			Title:     "Issue 3",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  1,
		},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("Failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Add labels to issues
	if err := store.AddLabel(ctx, "bd-batch1", "bug", "test-actor"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}
	if err := store.AddLabel(ctx, "bd-batch1", "urgent", "test-actor"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}
	if err := store.AddLabel(ctx, "bd-batch2", "feature", "test-actor"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}
	// bd-batch3 has no labels

	// Add comments to issues
	if _, err := store.AddIssueComment(ctx, "bd-batch1", "alice", "First comment"); err != nil {
		t.Fatalf("Failed to add comment: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, "bd-batch1", "bob", "Second comment"); err != nil {
		t.Fatalf("Failed to add comment: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, "bd-batch3", "charlie", "Comment on bd-batch3"); err != nil {
		t.Fatalf("Failed to add comment: %v", err)
	}
	// bd-batch2 has no comments

	// Test batch get labels
	issueIDs := []string{"bd-batch1", "bd-batch2", "bd-batch3"}
	allLabels, err := store.GetLabelsForIssues(ctx, issueIDs)
	if err != nil {
		t.Fatalf("GetLabelsForIssues failed: %v", err)
	}

	// Verify labels
	if len(allLabels["bd-batch1"]) != 2 {
		t.Errorf("Expected 2 labels for bd-batch1, got %d", len(allLabels["bd-batch1"]))
	}
	if len(allLabels["bd-batch2"]) != 1 {
		t.Errorf("Expected 1 label for bd-batch2, got %d", len(allLabels["bd-batch2"]))
	}
	if len(allLabels["bd-batch3"]) != 0 {
		t.Errorf("Expected 0 labels for bd-batch3, got %d", len(allLabels["bd-batch3"]))
	}

	// Test batch get comments
	allComments, err := store.GetCommentsForIssues(ctx, issueIDs)
	if err != nil {
		t.Fatalf("GetCommentsForIssues failed: %v", err)
	}

	// Verify comments
	if len(allComments["bd-batch1"]) != 2 {
		t.Errorf("Expected 2 comments for bd-batch1, got %d", len(allComments["bd-batch1"]))
	}
	if allComments["bd-batch1"][0].Author != "alice" {
		t.Errorf("Expected first comment author to be 'alice', got %s", allComments["bd-batch1"][0].Author)
	}
	if allComments["bd-batch1"][1].Author != "bob" {
		t.Errorf("Expected second comment author to be 'bob', got %s", allComments["bd-batch1"][1].Author)
	}

	if len(allComments["bd-batch2"]) != 0 {
		t.Errorf("Expected 0 comments for bd-batch2, got %d", len(allComments["bd-batch2"]))
	}

	if len(allComments["bd-batch3"]) != 1 {
		t.Errorf("Expected 1 comment for bd-batch3, got %d", len(allComments["bd-batch3"]))
	}
	if len(allComments["bd-batch3"]) > 0 && allComments["bd-batch3"][0].Author != "charlie" {
		t.Errorf("Expected comment author to be 'charlie', got %s", allComments["bd-batch3"][0].Author)
	}
}

// TestBatchGetEmptyIssueIDs verifies that batch methods handle empty issue ID lists
func TestBatchGetEmptyIssueIDs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Test with empty issue ID list
	labels, err := store.GetLabelsForIssues(ctx, []string{})
	if err != nil {
		t.Fatalf("GetLabelsForIssues with empty list failed: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("Expected empty map, got %d entries", len(labels))
	}

	comments, err := store.GetCommentsForIssues(ctx, []string{})
	if err != nil {
		t.Fatalf("GetCommentsForIssues with empty list failed: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("Expected empty map, got %d entries", len(comments))
	}
}
