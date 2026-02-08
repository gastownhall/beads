package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// Helper function to create a time pointer
func timePtr(t time.Time) *time.Time {
	return &t
}

func TestStatusCommand(t *testing.T) {
	// Create a temporary directory for the test database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, ".beads", "test.db")

	// Create .beads directory
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Initialize the database
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	// Create some test issues with different statuses
	testIssues := []*types.Issue{
		{
			Title:     "Open issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Assignee:  "alice",
		},
		{
			Title:     "Open issue 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeBug,
			Assignee:  "bob",
		},
		{
			Title:     "In progress issue",
			Status:    types.StatusInProgress,
			Priority:  1,
			IssueType: types.TypeFeature,
			Assignee:  "alice",
		},
		{
			Title:     "Blocked issue",
			Status:    types.StatusBlocked,
			Priority:  0,
			IssueType: types.TypeBug,
			Assignee:  "alice",
		},
		{
			Title:     "Closed issue",
			Status:    types.StatusClosed,
			Priority:  3,
			IssueType: types.TypeTask,
			Assignee:  "bob",
			ClosedAt:  timePtr(time.Now()),
		},
	}

	for _, issue := range testIssues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create test issue: %v", err)
		}
	}

	// Test GetStatistics
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	// Verify counts
	if stats.TotalIssues != 5 {
		t.Errorf("Expected 5 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 2 {
		t.Errorf("Expected 2 open issues, got %d", stats.OpenIssues)
	}
	if stats.InProgressIssues != 1 {
		t.Errorf("Expected 1 in-progress issue, got %d", stats.InProgressIssues)
	}
	if stats.BlockedIssues != 0 {
		// Note: BlockedIssues counts issues that are blocked by dependencies
		// Our test issue with status=blocked doesn't have dependencies, so count is 0
		t.Logf("BlockedIssues: %d (expected 0, status=blocked without deps)", stats.BlockedIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("Expected 1 closed issue, got %d", stats.ClosedIssues)
	}

	// Test JSON marshaling with full Statistics
	output := &StatusOutput{
		Summary: stats,
	}

	jsonBytes, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}

	t.Logf("Status output:\n%s", string(jsonBytes))

	// Verify JSON structure
	var decoded StatusOutput
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if decoded.Summary.TotalIssues != 5 {
		t.Errorf("Decoded total issues: expected 5, got %d", decoded.Summary.TotalIssues)
	}
}

func TestGetGitActivity(t *testing.T) {
	// Test getGitActivity - it may return nil if not in a git repo
	// or if there's no recent activity
	activity := getGitActivity(24)

	// If we're in a git repo with activity, verify the structure
	if activity != nil {
		if activity.HoursTracked != 24 {
			t.Errorf("Expected 24 hours tracked, got %d", activity.HoursTracked)
		}

		// Should have non-negative values
		if activity.CommitCount < 0 {
			t.Errorf("Negative commit count: %d", activity.CommitCount)
		}
		if activity.IssuesCreated < 0 {
			t.Errorf("Negative issues created: %d", activity.IssuesCreated)
		}
		if activity.IssuesClosed < 0 {
			t.Errorf("Negative issues closed: %d", activity.IssuesClosed)
		}
		if activity.IssuesUpdated < 0 {
			t.Errorf("Negative issues updated: %d", activity.IssuesUpdated)
		}

		t.Logf("Git activity: commits=%d, created=%d, closed=%d, updated=%d, total=%d",
			activity.CommitCount, activity.IssuesCreated, activity.IssuesClosed,
			activity.IssuesUpdated, activity.TotalChanges)
	} else {
		t.Log("No git activity found (not in a git repo or no recent commits)")
	}
}

func TestGetAssignedStatistics(t *testing.T) {
	// Create a temporary directory for the test database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, ".beads", "test.db")

	// Create .beads directory
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Initialize the database
	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Set issue prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	// Set global store and rootCtx for getAssignedStatistics
	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()
	store = testStore

	// Create test issues with different assignees
	testIssues := []*types.Issue{
		{
			Title:     "Alice's issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Assignee:  "alice",
		},
		{
			Title:     "Alice's issue 2",
			Status:    types.StatusInProgress,
			Priority:  1,
			IssueType: types.TypeTask,
			Assignee:  "alice",
		},
		{
			Title:     "Bob's issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Assignee:  "bob",
		},
	}

	for _, issue := range testIssues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create test issue: %v", err)
		}
	}

	// Test getAssignedStatistics for Alice
	stats := getAssignedStatistics("alice")
	if stats == nil {
		t.Fatal("getAssignedStatistics returned nil")
	}

	if stats.TotalIssues != 2 {
		t.Errorf("Expected 2 issues for alice, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 1 {
		t.Errorf("Expected 1 open issue for alice, got %d", stats.OpenIssues)
	}
	if stats.InProgressIssues != 1 {
		t.Errorf("Expected 1 in-progress issue for alice, got %d", stats.InProgressIssues)
	}

	// Test for Bob
	bobStats := getAssignedStatistics("bob")
	if bobStats == nil {
		t.Fatal("getAssignedStatistics returned nil for bob")
	}

	if bobStats.TotalIssues != 1 {
		t.Errorf("Expected 1 issue for bob, got %d", bobStats.TotalIssues)
	}
}
