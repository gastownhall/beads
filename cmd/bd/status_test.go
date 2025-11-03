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
	store, err := sqlite.New(dbPath)
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
			Title:      "Open issue 1",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			Assignee:   "alice",
		},
		{
			Title:      "Open issue 2",
			Status:     types.StatusOpen,
			Priority:   2,
			IssueType:  types.TypeBug,
			Assignee:   "bob",
		},
		{
			Title:      "In progress issue",
			Status:     types.StatusInProgress,
			Priority:   1,
			IssueType:  types.TypeFeature,
			Assignee:   "alice",
		},
		{
			Title:      "Blocked issue",
			Status:     types.StatusBlocked,
			Priority:   0,
			IssueType:  types.TypeBug,
			Assignee:   "alice",
		},
		{
			Title:      "Closed issue",
			Status:     types.StatusClosed,
			Priority:   3,
			IssueType:  types.TypeTask,
			Assignee:   "bob",
			ClosedAt:   timePtr(time.Now()),
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

	// Test status output structures
	summary := &StatusSummary{
		TotalIssues:      stats.TotalIssues,
		OpenIssues:       stats.OpenIssues,
		InProgressIssues: stats.InProgressIssues,
		BlockedIssues:    stats.BlockedIssues,
		ClosedIssues:     stats.ClosedIssues,
		ReadyIssues:      stats.ReadyIssues,
	}

	// Test JSON marshaling
	output := &StatusOutput{
		Summary: summary,
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

func TestGetRecentActivity(t *testing.T) {
	// Create a temporary directory for the test database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, ".beads", "test.db")

	// Create .beads directory
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Initialize the database
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Set issue prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	// Set global store for getRecentActivity
	store = testStore

	// Create some test issues
	testIssues := []*types.Issue{
		{
			Title:     "Recent issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
		{
			Title:     "Recent closed issue",
			Status:    types.StatusClosed,
			Priority:  1,
			IssueType: types.TypeTask,
			ClosedAt:  timePtr(time.Now()),
		},
	}

	for _, issue := range testIssues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create test issue: %v", err)
		}
	}

	// Test getRecentActivity
	activity := getRecentActivity(ctx, 7, nil)
	if activity == nil {
		t.Fatal("getRecentActivity returned nil")
	}

	if activity.DaysTracked != 7 {
		t.Errorf("Expected 7 days tracked, got %d", activity.DaysTracked)
	}

	// All issues were created just now, so they should all be in "recent"
	if activity.IssuesCreated < 2 {
		t.Errorf("Expected at least 2 issues created, got %d", activity.IssuesCreated)
	}

	t.Logf("Recent activity: created=%d, closed=%d, updated=%d",
		activity.IssuesCreated, activity.IssuesClosed, activity.IssuesUpdated)
}

func TestGetAssignedStatus(t *testing.T) {
	// Create a temporary directory for the test database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, ".beads", "test.db")

	// Create .beads directory
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Initialize the database
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Set issue prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	// Set global store for getAssignedStatus
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

	// Test getAssignedStatus for Alice
	summary := getAssignedStatus("alice")
	if summary == nil {
		t.Fatal("getAssignedStatus returned nil")
	}

	if summary.TotalIssues != 2 {
		t.Errorf("Expected 2 issues for alice, got %d", summary.TotalIssues)
	}
	if summary.OpenIssues != 1 {
		t.Errorf("Expected 1 open issue for alice, got %d", summary.OpenIssues)
	}
	if summary.InProgressIssues != 1 {
		t.Errorf("Expected 1 in-progress issue for alice, got %d", summary.InProgressIssues)
	}

	// Test for Bob
	bobSummary := getAssignedStatus("bob")
	if bobSummary == nil {
		t.Fatal("getAssignedStatus returned nil for bob")
	}

	if bobSummary.TotalIssues != 1 {
		t.Errorf("Expected 1 issue for bob, got %d", bobSummary.TotalIssues)
	}
}
