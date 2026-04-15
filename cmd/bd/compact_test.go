//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestCompactSuite(t *testing.T) {
	// Compaction is now implemented for Dolt backend
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("DryRun", func(t *testing.T) {
		// Create a closed issue
		issue := &types.Issue{
			ID:          "test-dryrun-1",
			Title:       "Test Issue",
			Description: "This is a long description that should be compacted. " + string(make([]byte, 500)),
			Status:      types.StatusClosed,
			Priority:    2,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
			ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}

		// Test dry run - should check eligibility without error even without API key
		eligible, reason, err := s.CheckEligibility(ctx, "test-dryrun-1", 1)
		if err != nil {
			t.Fatalf("CheckEligibility failed: %v", err)
		}

		if !eligible {
			t.Fatalf("Issue should be eligible for compaction: %s", reason)
		}
	})

	t.Run("Stats", func(t *testing.T) {
		// Create mix of issues - some eligible, some not
		issues := []*types.Issue{
			{
				ID:          "test-stats-1",
				Title:       "Old closed",
				Description: "Content that makes this issue eligible for compaction.",
				Status:      types.StatusClosed,
				Priority:    2,
				IssueType:   types.TypeTask,
				CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
				ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
			},
			{
				ID:          "test-stats-2",
				Title:       "Recent closed",
				Description: "Some content here too.",
				Status:      types.StatusClosed,
				Priority:    2,
				IssueType:   types.TypeTask,
				CreatedAt:   time.Now().Add(-10 * 24 * time.Hour),
				ClosedAt:    ptrTime(time.Now().Add(-5 * 24 * time.Hour)),
			},
			{
				ID:        "test-stats-3",
				Title:     "Still open",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now().Add(-40 * 24 * time.Hour),
			},
		}

		for _, issue := range issues {
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Verify issues were created
		allIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		// Count issues with stats prefix
		statCount := 0
		for _, issue := range allIssues {
			if len(issue.ID) >= 11 && issue.ID[:11] == "test-stats-" {
				statCount++
			}
		}

		if statCount != 3 {
			t.Errorf("Expected 3 stats issues, got %d", statCount)
		}

		// Test eligibility check for old closed issue
		eligible, _, err := s.CheckEligibility(ctx, "test-stats-1", 1)
		if err != nil {
			t.Fatalf("CheckEligibility failed: %v", err)
		}
		if !eligible {
			t.Error("Old closed issue should be eligible for Tier 1")
		}
	})

	t.Run("RunCompactStats", func(t *testing.T) {
		// Create some closed issues
		for i := 1; i <= 3; i++ {
			id := fmt.Sprintf("test-runstats-%d", i)
			issue := &types.Issue{
				ID:          id,
				Title:       "Test Issue",
				Description: string(make([]byte, 500)),
				Status:      types.StatusClosed,
				Priority:    2,
				IssueType:   types.TypeTask,
				CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
				ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
			}
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Test stats - should work without API key
		savedJSONOutput := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = savedJSONOutput }()

		// Actually call runCompactStats to increase coverage
		runCompactStats(ctx, s)

		// Also test with JSON output
		jsonOutput = true
		runCompactStats(ctx, s)
	})

	t.Run("CompactStatsJSON", func(t *testing.T) {
		// Create a closed issue eligible for Tier 1
		issue := &types.Issue{
			ID:          "test-json-1",
			Title:       "Test Issue",
			Description: string(make([]byte, 500)),
			Status:      types.StatusClosed,
			Priority:    2,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
			ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}

		// Test with JSON output
		savedJSONOutput := jsonOutput
		jsonOutput = true
		defer func() { jsonOutput = savedJSONOutput }()

		// Should not panic and should execute JSON path
		runCompactStats(ctx, s)
	})

	t.Run("RunCompactSingleDryRun", func(t *testing.T) {
		// Create a closed issue eligible for compaction
		issue := &types.Issue{
			ID:          "test-single-1",
			Title:       "Test Compact Issue",
			Description: string(make([]byte, 500)),
			Status:      types.StatusClosed,
			Priority:    2,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
			ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}

		// Test eligibility in dry run mode
		eligible, _, err := s.CheckEligibility(ctx, "test-single-1", 1)
		if err != nil {
			t.Fatalf("CheckEligibility failed: %v", err)
		}
		if !eligible {
			t.Error("Issue should be eligible for Tier 1 compaction")
		}
	})

	t.Run("RunCompactAllDryRun", func(t *testing.T) {
		// Create multiple closed issues
		for i := 1; i <= 3; i++ {
			issue := &types.Issue{
				ID:          fmt.Sprintf("test-all-%d", i),
				Title:       "Test Issue",
				Description: string(make([]byte, 500)),
				Status:      types.StatusClosed,
				Priority:    2,
				IssueType:   types.TypeTask,
				CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
				ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
			}
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Verify issues eligible for compaction
		closedStatus := types.StatusClosed
		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{Status: &closedStatus})
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		eligibleCount := 0
		for _, issue := range issues {
			// Only count our test-all issues
			if len(issue.ID) < 9 || issue.ID[:9] != "test-all-" {
				continue
			}
			eligible, _, err := s.CheckEligibility(ctx, issue.ID, 1)
			if err != nil {
				t.Fatalf("CheckEligibility failed for %s: %v", issue.ID, err)
			}
			if eligible {
				eligibleCount++
			}
		}

		if eligibleCount != 3 {
			t.Errorf("Expected 3 eligible issues, got %d", eligibleCount)
		}
	})
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func TestCompactInitCommand(t *testing.T) {
	if compactCmd == nil {
		t.Fatal("compactCmd should be initialized")
	}

	if compactCmd.Use != "compact" {
		t.Errorf("Expected Use='compact', got %q", compactCmd.Use)
	}

	if len(compactCmd.Long) == 0 {
		t.Error("compactCmd should have Long description")
	}

	// Verify --json flag exists
	jsonFlag := compactCmd.Flags().Lookup("json")
	if jsonFlag == nil {
		t.Error("compact command should have --json flag")
	}
}
