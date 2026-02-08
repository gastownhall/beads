package memory

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestHyphenatedPrefix(t *testing.T) {
	store := New("")
	ctx := context.Background()

	// Set hyphenated issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "my-app"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	issue := &types.Issue{
		Title:     "Hyphenated issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if issue.ID != "my-app-1" {
		t.Errorf("Expected ID 'my-app-1', got '%s'", issue.ID)
	}

	// Verify we can extract it back
	// This relies on internal extractPrefixAndNumber which we suspect is broken
	// But strictly speaking, CreateIssue uses the counter to generate ID.
	// The counter key relies on prefix extraction if we are incrementing.

	// Let's create another issue to see if it increments correctly.
	// If extractPrefixAndNumber is broken, it might fail to find the last number and might restart or fail.

	issue2 := &types.Issue{
		Title:     "Hyphenated issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	err = store.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue 2 failed: %v", err)
	}

	if issue2.ID != "my-app-2" {
		t.Errorf("Expected ID 'my-app-2', got '%s'", issue2.ID)
	}
}

func TestLoadHyphenatedIssues(t *testing.T) {
	store := New("")
	defer store.Close()

	issues := []*types.Issue{
		{
			ID:        "my-app-5",
			Title:     "Issue 5",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
	}

	if err := store.LoadFromIssues(issues); err != nil {
		t.Fatalf("LoadFromIssues failed: %v", err)
	}

	// Counter should be 5 for "my-app"

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "my-app"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	newIssue := &types.Issue{
		Title:     "New Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	if err := store.CreateIssue(ctx, newIssue, "user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Should be my-app-6
	if newIssue.ID != "my-app-6" {
		t.Errorf("Expected ID 'my-app-6', got '%s'", newIssue.ID)
	}
}
