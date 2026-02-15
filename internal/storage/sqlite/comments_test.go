package sqlite

import (
	"context"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestAddIssueComment tests basic comment addition
func TestAddIssueComment(t *testing.T) {
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
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add a comment
	comment, err := store.AddIssueComment(ctx, issue.ID, "alice", "This is a test comment")
	if err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}

	// Verify comment fields
	if comment.IssueID != issue.ID {
		t.Errorf("Expected IssueID %s, got %s", issue.ID, comment.IssueID)
	}
	if comment.Author != "alice" {
		t.Errorf("Expected Author 'alice', got '%s'", comment.Author)
	}
	if comment.Text != "This is a test comment" {
		t.Errorf("Expected Text 'This is a test comment', got '%s'", comment.Text)
	}
	if comment.ID == 0 {
		t.Error("Expected non-zero comment ID")
	}
	if comment.CreatedAt.IsZero() {
		t.Error("Expected non-zero CreatedAt timestamp")
	}
}

// TestAddIssueCommentNonexistentIssue tests adding comment to non-existent issue
func TestAddIssueCommentNonexistentIssue(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Try to add comment to non-existent issue
	_, err := store.AddIssueComment(ctx, "nonexistent-id", "alice", "comment")
	if err == nil {
		t.Fatal("Expected error when adding comment to non-existent issue, got nil")
	}
}

// TestGetIssueComments tests retrieving comments
func TestGetIssueComments(t *testing.T) {
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
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add multiple comments
	testComments := []struct {
		author string
		text   string
	}{
		{"alice", "First comment"},
		{"bob", "Second comment"},
		{"charlie", "Third comment"},
	}

	for _, tc := range testComments {
		_, err := store.AddIssueComment(ctx, issue.ID, tc.author, tc.text)
		if err != nil {
			t.Fatalf("AddIssueComment failed: %v", err)
		}
	}

	// Retrieve comments
	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	// Verify number of comments
	if len(comments) != len(testComments) {
		t.Fatalf("Expected %d comments, got %d", len(testComments), len(comments))
	}

	// Verify comment content and ordering (should be chronological)
	for i, comment := range comments {
		if comment.Author != testComments[i].author {
			t.Errorf("Comment %d: expected author %s, got %s", i, testComments[i].author, comment.Author)
		}
		if comment.Text != testComments[i].text {
			t.Errorf("Comment %d: expected text %s, got %s", i, testComments[i].text, comment.Text)
		}
		if comment.IssueID != issue.ID {
			t.Errorf("Comment %d: expected IssueID %s, got %s", i, issue.ID, comment.IssueID)
		}
	}
}

// TestGetIssueCommentsOrdering tests that comments are returned in chronological order
func TestGetIssueCommentsOrdering(t *testing.T) {
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
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add comments with identifiable ordering
	for i := 1; i <= 5; i++ {
		text := "Comment " + strconv.Itoa(i)
		_, err := store.AddIssueComment(ctx, issue.ID, "alice", text)
		if err != nil {
			t.Fatalf("AddIssueComment failed: %v", err)
		}
	}

	// Retrieve comments
	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	// Verify chronological ordering
	if len(comments) != 5 {
		t.Fatalf("Expected 5 comments, got %d", len(comments))
	}

	for i := 0; i < len(comments); i++ {
		expectedText := "Comment " + strconv.Itoa(i+1)
		if comments[i].Text != expectedText {
			t.Errorf("Comment %d: expected text %s, got %s", i, expectedText, comments[i].Text)
		}

		// Verify timestamps are in ascending order
		if i > 0 && comments[i].CreatedAt.Before(comments[i-1].CreatedAt) {
			t.Errorf("Comments not in chronological order: comment %d (%v) is before comment %d (%v)",
				i, comments[i].CreatedAt, i-1, comments[i-1].CreatedAt)
		}
	}
}

// TestGetIssueCommentsEmpty tests retrieving comments for issue with no comments
func TestGetIssueCommentsEmpty(t *testing.T) {
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
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Retrieve comments (should be empty)
	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	if len(comments) != 0 {
		t.Errorf("Expected 0 comments, got %d", len(comments))
	}
}

// TestGetIssueCommentsNonexistentIssue tests retrieving comments for non-existent issue
func TestGetIssueCommentsNonexistentIssue(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Retrieve comments for non-existent issue
	comments, err := store.GetIssueComments(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	// Should return empty slice, not error
	if len(comments) != 0 {
		t.Errorf("Expected 0 comments for non-existent issue, got %d", len(comments))
	}
}

// TestAddIssueCommentEmptyText tests adding comment with empty text
func TestAddIssueCommentEmptyText(t *testing.T) {
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
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add comment with empty text (should succeed - validation is caller's responsibility)
	comment, err := store.AddIssueComment(ctx, issue.ID, "alice", "")
	if err != nil {
		t.Fatalf("AddIssueComment with empty text failed: %v", err)
	}

	if comment.Text != "" {
		t.Errorf("Expected empty text, got '%s'", comment.Text)
	}
}

// TestGetIssueCommentsMultipleIssues tests that comments are properly isolated per issue
func TestGetIssueCommentsMultipleIssues(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two issues
	issue1 := &types.Issue{
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue2 := &types.Issue{
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue1, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add comments to each issue
	_, err := store.AddIssueComment(ctx, issue1.ID, "alice", "Comment for issue 1")
	if err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}
	_, err = store.AddIssueComment(ctx, issue1.ID, "bob", "Another comment for issue 1")
	if err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}
	_, err = store.AddIssueComment(ctx, issue2.ID, "charlie", "Comment for issue 2")
	if err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}

	// Retrieve comments for issue 1
	comments1, err := store.GetIssueComments(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	// Retrieve comments for issue 2
	comments2, err := store.GetIssueComments(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	// Verify each issue has the correct number of comments
	if len(comments1) != 2 {
		t.Errorf("Expected 2 comments for issue 1, got %d", len(comments1))
	}
	if len(comments2) != 1 {
		t.Errorf("Expected 1 comment for issue 2, got %d", len(comments2))
	}

	// Verify comments belong to correct issues
	for _, c := range comments1 {
		if c.IssueID != issue1.ID {
			t.Errorf("Comment has wrong IssueID: expected %s, got %s", issue1.ID, c.IssueID)
		}
	}
	for _, c := range comments2 {
		if c.IssueID != issue2.ID {
			t.Errorf("Comment has wrong IssueID: expected %s, got %s", issue2.ID, c.IssueID)
		}
	}
}
