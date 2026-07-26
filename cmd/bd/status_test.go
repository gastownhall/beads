//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
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
	store, err := dolt.New(context.Background(), &dolt.Config{Path: dbPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
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
	if stats.BlockedIssues != nil && *stats.BlockedIssues != 0 {
		// Note: BlockedIssues counts issues that are blocked by dependencies
		// Our test issue with status=blocked doesn't have dependencies, so count is 0
		t.Logf("BlockedIssues: %d (expected 0, status=blocked without deps)", *stats.BlockedIssues)
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
	testStore, err := dolt.New(context.Background(), &dolt.Config{Path: dbPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
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

// TestRenderStatus_SkipJSONEmitsNullNotZero verifies that when BlockedIssues/
// ReadyIssues are nil (the --no-blocked shape), the JSON envelope reports
// blocked_count_skipped:true and emits literal `null` for both fields rather
// than a fake 0 that a CI consumer could misread as "nothing blocked / ready".
func TestRenderStatus_SkipJSONEmitsNullNotZero(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = oldJSON }()

	stats := &types.Statistics{
		TotalIssues:   3,
		OpenIssues:    2,
		ClosedIssues:  1,
		BlockedIssues: nil,
		ReadyIssues:   nil,
	}

	out := captureStdout(t, func() error {
		return renderStatus(stats, nil)
	})

	var decoded struct {
		BlockedCountSkipped bool `json:"blocked_count_skipped"`
		Summary             struct {
			BlockedIssues *int `json:"blocked_issues"`
			ReadyIssues   *int `json:"ready_issues"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("failed to unmarshal renderStatus JSON output: %v\nraw: %s", err, out)
	}

	if !decoded.BlockedCountSkipped {
		t.Errorf("expected blocked_count_skipped: true, got false\nraw: %s", out)
	}
	if decoded.Summary.BlockedIssues != nil {
		t.Errorf("expected summary.blocked_issues: null, got %d\nraw: %s", *decoded.Summary.BlockedIssues, out)
	}
	if decoded.Summary.ReadyIssues != nil {
		t.Errorf("expected summary.ready_issues: null, got %d\nraw: %s", *decoded.Summary.ReadyIssues, out)
	}

	// Literal "null" must actually be present in the raw bytes -- a stray
	// custom MarshalJSON or a non-pointer regression would silently coerce
	// this back to 0 without failing the struct-decode assertions above.
	if !strings.Contains(out, `"blocked_issues": null`) {
		t.Errorf("expected literal \"blocked_issues\": null in raw JSON, got:\n%s", out)
	}
	if !strings.Contains(out, `"ready_issues": null`) {
		t.Errorf("expected literal \"ready_issues\": null in raw JSON, got:\n%s", out)
	}
}

// TestRenderStatus_SkipHumanRendersSkippedNotZero verifies the human-readable
// branch renders "(skipped)" for Blocked and Ready to Work when their stats
// fields are nil, derived from the data (nil pointers) rather than a
// separately-tracked flag -- so it stays correct even when a caller (like
// --assigned) recomputes fully-populated stats.
func TestRenderStatus_SkipHumanRendersSkippedNotZero(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = oldJSON }()

	stats := &types.Statistics{
		TotalIssues:   3,
		OpenIssues:    2,
		ClosedIssues:  1,
		BlockedIssues: nil,
		ReadyIssues:   nil,
	}

	out := captureStdout(t, func() error {
		return renderStatus(stats, nil)
	})

	if n := strings.Count(out, "(skipped)"); n != 2 {
		t.Errorf("expected \"(skipped)\" to render twice (Blocked + Ready to Work), got %d times:\n%s", n, out)
	}
}

// TestRenderStatus_AssignedIgnoresSkipEvenWithNoBlockedFlag guards the
// data-derived skip-state fix directly: fully-populated stats (as
// getAssignedStatistics/buildAssignedStats always produce) must never render
// "(skipped)", regardless of what --no-blocked was passed on the command
// line -- renderStatus no longer takes a noBlocked flag at all.
func TestRenderStatus_AssignedIgnoresSkipEvenWithNoBlockedFlag(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = oldJSON }()

	stats := buildAssignedStats([]*types.Issue{
		{Status: types.StatusOpen},
		{Status: types.StatusBlocked},
	}, 1)
	if stats.BlockedIssues == nil || stats.ReadyIssues == nil {
		t.Fatalf("buildAssignedStats should always populate BlockedIssues/ReadyIssues; got %+v", stats)
	}

	out := captureStdout(t, func() error {
		return renderStatus(stats, nil)
	})

	if strings.Contains(out, "(skipped)") {
		t.Errorf("expected no \"(skipped)\" rendering for fully-populated assigned stats, got:\n%s", out)
	}
}

// TestGetStatisticsNoBlocked verifies the --no-blocked fast path leaves
// BlockedIssues and ReadyIssues nil, while the same store's full GetStatistics
// call populates both -- guarding the *int fake-zero regression this PR fixes.
func TestGetStatisticsNoBlocked(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, ".beads", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	testStore, err := dolt.New(context.Background(), &dolt.Config{Path: dbPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	if err := testStore.CreateIssue(ctx, &types.Issue{
		Title:     "Open issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	noBlocked, err := testStore.GetStatisticsNoBlocked(ctx)
	if err != nil {
		t.Fatalf("GetStatisticsNoBlocked failed: %v", err)
	}
	if noBlocked.BlockedIssues != nil {
		t.Errorf("expected BlockedIssues nil under --no-blocked, got %d", *noBlocked.BlockedIssues)
	}
	if noBlocked.ReadyIssues != nil {
		t.Errorf("expected ReadyIssues nil under --no-blocked, got %d", *noBlocked.ReadyIssues)
	}

	full, err := testStore.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}
	if full.BlockedIssues == nil {
		t.Fatal("expected BlockedIssues populated by full GetStatistics, got nil")
	}
	if full.ReadyIssues == nil {
		t.Fatal("expected ReadyIssues populated by full GetStatistics, got nil")
	}
}
