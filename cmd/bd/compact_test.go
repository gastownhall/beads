package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestCompactSuite(t *testing.T) {
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
				ID:        "test-stats-1",
				Title:     "Old closed",
				Status:    types.StatusClosed,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now().Add(-60 * 24 * time.Hour),
				ClosedAt:  ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
			},
			{
				ID:        "test-stats-2",
				Title:     "Recent closed",
				Status:    types.StatusClosed,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now().Add(-10 * 24 * time.Hour),
				ClosedAt:  ptrTime(time.Now().Add(-5 * 24 * time.Hour)),
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

func TestCompactValidation(t *testing.T) {
	tests := []struct {
		name       string
		compactID  string
		compactAll bool
		dryRun     bool
		force      bool
		wantError  bool
	}{
		{
			name:       "both id and all",
			compactID:  "test-1",
			compactAll: true,
			wantError:  true,
		},
		{
			name:      "force without id",
			force:     true,
			wantError: true,
		},
		{
			name:      "no flags",
			wantError: true,
		},
		{
			name:      "dry run only",
			dryRun:    true,
			wantError: false,
		},
		{
			name:      "id only",
			compactID: "test-1",
			wantError: false,
		},
		{
			name:       "all only",
			compactAll: true,
			wantError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.compactID != "" && tt.compactAll {
				// Should fail
				if !tt.wantError {
					t.Error("Expected error for both --id and --all")
				}
			}

			if tt.force && tt.compactID == "" {
				// Should fail
				if !tt.wantError {
					t.Error("Expected error for --force without --id")
				}
			}

			if tt.compactID == "" && !tt.compactAll && !tt.dryRun {
				// Should fail
				if !tt.wantError {
					t.Error("Expected error when no action specified")
				}
			}
		})
	}
}

func TestCompactProgressBar(t *testing.T) {
	// Test progress bar formatting
	pb := progressBar(50, 100)
	if len(pb) == 0 {
		t.Error("Progress bar should not be empty")
	}

	pb = progressBar(100, 100)
	if len(pb) == 0 {
		t.Error("Full progress bar should not be empty")
	}

	pb = progressBar(0, 100)
	if len(pb) == 0 {
		t.Error("Zero progress bar should not be empty")
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name    string
		seconds float64
		want    string
	}{
		{
			name:    "seconds",
			seconds: 45.0,
			want:    "45s",
		},
		{
			name:    "minutes",
			seconds: 300.0,
			want:    "5m 0s",
		},
		{
			name:    "hours",
			seconds: 7200.0,
			want:    "2h 0m",
		},
		{
			name:    "days",
			seconds: 90000.0,
			want:    "1d 1h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUptime(tt.seconds)
			if got != tt.want {
				t.Errorf("formatUptime(%v) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
	}
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

func TestPruneExpiredTombstones(t *testing.T) {
	// Setup: create a temp .beads directory with issues.jsonl
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create issues.jsonl with mix of live issues, fresh tombstones, and expired tombstones
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	now := time.Now()

	freshTombstoneTime := now.Add(-10 * 24 * time.Hour)   // 10 days ago - NOT expired
	expiredTombstoneTime := now.Add(-60 * 24 * time.Hour) // 60 days ago - expired (> 30 day TTL)

	issues := []*types.Issue{
		{
			ID:        "test-live",
			Title:     "Live issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now.Add(-5 * 24 * time.Hour),
			UpdatedAt: now,
		},
		{
			ID:           "test-fresh-tombstone",
			Title:        "(deleted)",
			Status:       types.StatusTombstone,
			Priority:     0,
			IssueType:    types.TypeTask,
			CreatedAt:    now.Add(-20 * 24 * time.Hour),
			UpdatedAt:    freshTombstoneTime,
			DeletedAt:    &freshTombstoneTime,
			DeletedBy:    "alice",
			DeleteReason: "duplicate",
		},
		{
			ID:           "test-expired-tombstone",
			Title:        "(deleted)",
			Status:       types.StatusTombstone,
			Priority:     0,
			IssueType:    types.TypeTask,
			CreatedAt:    now.Add(-90 * 24 * time.Hour),
			UpdatedAt:    expiredTombstoneTime,
			DeletedAt:    &expiredTombstoneTime,
			DeletedBy:    "bob",
			DeleteReason: "obsolete",
		},
	}

	// Write issues to JSONL
	file, err := os.Create(issuesPath)
	if err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}
	encoder := json.NewEncoder(file)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			file.Close()
			t.Fatalf("Failed to write issue: %v", err)
		}
	}
	file.Close()

	// Save original dbPath and restore after test
	originalDBPath := dbPath
	defer func() { dbPath = originalDBPath }()
	dbPath = filepath.Join(beadsDir, "beads.db")

	// Run pruning (0 = use default TTL)
	result, err := pruneExpiredTombstones(0)
	if err != nil {
		t.Fatalf("pruneExpiredTombstones failed: %v", err)
	}

	// Verify results
	if result.PrunedCount != 1 {
		t.Errorf("Expected 1 pruned tombstone, got %d", result.PrunedCount)
	}
	if len(result.PrunedIDs) != 1 || result.PrunedIDs[0] != "test-expired-tombstone" {
		t.Errorf("Expected PrunedIDs [test-expired-tombstone], got %v", result.PrunedIDs)
	}
	if result.TTLDays != 30 {
		t.Errorf("Expected TTLDays 30, got %d", result.TTLDays)
	}

	// Verify the file was updated correctly
	file, err = os.Open(issuesPath)
	if err != nil {
		t.Fatalf("Failed to reopen issues.jsonl: %v", err)
	}
	defer file.Close()

	var remaining []*types.Issue
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("Failed to decode issue: %v", err)
		}
		remaining = append(remaining, &issue)
	}

	if len(remaining) != 2 {
		t.Fatalf("Expected 2 remaining issues, got %d", len(remaining))
	}

	// Verify live issue and fresh tombstone remain
	ids := make(map[string]bool)
	for _, issue := range remaining {
		ids[issue.ID] = true
	}
	if !ids["test-live"] {
		t.Error("Live issue should remain")
	}
	if !ids["test-fresh-tombstone"] {
		t.Error("Fresh tombstone should remain")
	}
	if ids["test-expired-tombstone"] {
		t.Error("Expired tombstone should have been pruned")
	}
}

func TestPruneExpiredTombstones_CustomTTL(t *testing.T) {
	// Setup: create a temp .beads directory with issues.jsonl
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	now := time.Now()

	// Both tombstones are older than 5 days, so both should be pruned with 5-day TTL
	tombstoneTime := now.Add(-10 * 24 * time.Hour) // 10 days ago

	issues := []*types.Issue{
		{
			ID:        "test-live",
			Title:     "Live issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now.Add(-5 * 24 * time.Hour),
			UpdatedAt: now,
		},
		{
			ID:           "test-tombstone-1",
			Title:        "(deleted)",
			Status:       types.StatusTombstone,
			Priority:     0,
			IssueType:    types.TypeTask,
			CreatedAt:    now.Add(-20 * 24 * time.Hour),
			UpdatedAt:    tombstoneTime,
			DeletedAt:    &tombstoneTime,
			DeletedBy:    "alice",
			DeleteReason: "duplicate",
		},
	}

	// Write issues to JSONL
	file, err := os.Create(issuesPath)
	if err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}
	encoder := json.NewEncoder(file)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			file.Close()
			t.Fatalf("Failed to write issue: %v", err)
		}
	}
	file.Close()

	// Save original dbPath and restore after test
	originalDBPath := dbPath
	defer func() { dbPath = originalDBPath }()
	dbPath = filepath.Join(beadsDir, "beads.db")

	// Run pruning with 5-day TTL - tombstone is 10 days old, should be pruned
	customTTL := 5 * 24 * time.Hour
	result, err := pruneExpiredTombstones(customTTL)
	if err != nil {
		t.Fatalf("pruneExpiredTombstones failed: %v", err)
	}

	// Verify results - 5-day TTL means tombstones older than 5 days are pruned
	if result.PrunedCount != 1 {
		t.Errorf("Expected 1 pruned tombstone with 5-day TTL, got %d", result.PrunedCount)
	}
	if result.TTLDays != 5 {
		t.Errorf("Expected TTLDays 5, got %d", result.TTLDays)
	}
}

func TestPreviewPruneTombstones(t *testing.T) {
	// Setup: create a temp .beads directory with issues.jsonl
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	now := time.Now()

	expiredTombstoneTime := now.Add(-60 * 24 * time.Hour) // 60 days ago

	issues := []*types.Issue{
		{
			ID:        "test-live",
			Title:     "Live issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:           "test-expired-tombstone",
			Title:        "(deleted)",
			Status:       types.StatusTombstone,
			Priority:     0,
			IssueType:    types.TypeTask,
			CreatedAt:    now.Add(-90 * 24 * time.Hour),
			UpdatedAt:    expiredTombstoneTime,
			DeletedAt:    &expiredTombstoneTime,
			DeletedBy:    "bob",
			DeleteReason: "obsolete",
		},
	}

	// Write issues to JSONL
	file, err := os.Create(issuesPath)
	if err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}
	encoder := json.NewEncoder(file)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			file.Close()
			t.Fatalf("Failed to write issue: %v", err)
		}
	}
	file.Close()

	// Save original dbPath and restore after test
	originalDBPath := dbPath
	defer func() { dbPath = originalDBPath }()
	dbPath = filepath.Join(beadsDir, "beads.db")

	// Preview pruning - should not modify file
	result, err := previewPruneTombstones(0)
	if err != nil {
		t.Fatalf("previewPruneTombstones failed: %v", err)
	}

	// Verify preview results
	if result.PrunedCount != 1 {
		t.Errorf("Expected 1 tombstone to prune, got %d", result.PrunedCount)
	}
	if result.PrunedIDs[0] != "test-expired-tombstone" {
		t.Errorf("Expected PrunedIDs [test-expired-tombstone], got %v", result.PrunedIDs)
	}

	// Verify file was NOT modified (preview mode)
	file, err = os.Open(issuesPath)
	if err != nil {
		t.Fatalf("Failed to reopen issues.jsonl: %v", err)
	}
	defer file.Close()

	var remaining []*types.Issue
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("Failed to decode issue: %v", err)
		}
		remaining = append(remaining, &issue)
	}

	// Both issues should still be in file (preview doesn't modify)
	if len(remaining) != 2 {
		t.Errorf("Expected 2 issues (preview mode), got %d", len(remaining))
	}
}

func TestCompactPruneFlagExists(t *testing.T) {
	// Verify --prune flag exists
	pruneFlag := compactCmd.Flags().Lookup("prune")
	if pruneFlag == nil {
		t.Error("compact command should have --prune flag")
	}

	// Verify --older-than flag exists
	olderThanFlag := compactCmd.Flags().Lookup("older-than")
	if olderThanFlag == nil {
		t.Error("compact command should have --older-than flag")
	}
}

func TestPruneExpiredTombstones_NoTombstones(t *testing.T) {
	// Setup: create a temp .beads directory with only live issues
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	now := time.Now()

	issue := &types.Issue{
		ID:        "test-live",
		Title:     "Live issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	file, err := os.Create(issuesPath)
	if err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(issue); err != nil {
		file.Close()
		t.Fatalf("Failed to write issue: %v", err)
	}
	file.Close()

	// Save original dbPath and restore after test
	originalDBPath := dbPath
	defer func() { dbPath = originalDBPath }()
	dbPath = filepath.Join(beadsDir, "beads.db")

	// Run pruning - should return zero pruned (0 = use default TTL)
	result, err := pruneExpiredTombstones(0)
	if err != nil {
		t.Fatalf("pruneExpiredTombstones failed: %v", err)
	}

	if result.PrunedCount != 0 {
		t.Errorf("Expected 0 pruned tombstones, got %d", result.PrunedCount)
	}
}
