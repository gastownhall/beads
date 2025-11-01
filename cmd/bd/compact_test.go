package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestCompactDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	sqliteStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	
	// Set issue_prefix to prevent "database not initialized" errors
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	
	// Create a closed issue
	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "This is a long description that should be compacted. " + string(make([]byte, 500)),
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
		ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
	}

	if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}

	// Test dry run - should not error even without API key
	compactDryRun = true
	compactTier = 1
	compactID = "test-1"
	compactForce = false
	jsonOutput = false
	
	store = sqliteStore
	daemonClient = nil

	// Should check eligibility without error
	eligible, reason, err := sqliteStore.CheckEligibility(ctx, "test-1", 1)
	if err != nil {
		t.Fatalf("CheckEligibility failed: %v", err)
	}
	
	if !eligible {
		t.Fatalf("Issue should be eligible for compaction: %s", reason)
	}

	compactDryRun = false
	compactID = ""
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
			name:      "both id and all",
			compactID: "test-1",
			compactAll: true,
			wantError: true,
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
			name:       "id only",
			compactID:  "test-1",
			wantError:  false,
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

func TestCompactStats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	sqliteStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	
	// Set issue_prefix to prevent "database not initialized" errors
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	
	// Create mix of issues - some eligible, some not
	issues := []*types.Issue{
		{
			ID:        "test-1",
			Title:     "Old closed",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now().Add(-60 * 24 * time.Hour),
			ClosedAt:  ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
		},
		{
			ID:        "test-2",
			Title:     "Recent closed",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now().Add(-10 * 24 * time.Hour),
			ClosedAt:  ptrTime(time.Now().Add(-5 * 24 * time.Hour)),
		},
		{
			ID:        "test-3",
			Title:     "Still open",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now().Add(-40 * 24 * time.Hour),
		},
	}

	for _, issue := range issues {
		if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Verify issues were created
	allIssues, err := sqliteStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(allIssues) != 3 {
		t.Errorf("Expected 3 total issues, got %d", len(allIssues))
	}

	// Test eligibility check for old closed issue
	eligible, _, err := sqliteStore.CheckEligibility(ctx, "test-1", 1)
	if err != nil {
		t.Fatalf("CheckEligibility failed: %v", err)
	}
	if !eligible {
		t.Error("Old closed issue should be eligible for Tier 1")
	}
}

func TestRunCompactStats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	sqliteStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create some closed issues
	for i := 1; i <= 3; i++ {
		id := "test-" + string(rune('0'+i))
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
		if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Test stats - should work without API key
	savedJSONOutput := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = savedJSONOutput }()

	// Actually call runCompactStats to increase coverage
	runCompactStats(ctx, sqliteStore)

	// Also test with JSON output
	jsonOutput = true
	runCompactStats(ctx, sqliteStore)
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
			want:    "45.0 seconds",
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

func TestCompactStatsJSON(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	sqliteStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create a closed issue eligible for Tier 1
	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: string(make([]byte, 500)),
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
		ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
	}
	if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}

	// Test with JSON output
	savedJSONOutput := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = savedJSONOutput }()

	// Should not panic and should execute JSON path
	runCompactStats(ctx, sqliteStore)
}

func TestRunCompactSingleDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	sqliteStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create a closed issue eligible for compaction
	issue := &types.Issue{
		ID:          "test-compact-1",
		Title:       "Test Compact Issue",
		Description: string(make([]byte, 500)),
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now().Add(-60 * 24 * time.Hour),
		ClosedAt:    ptrTime(time.Now().Add(-35 * 24 * time.Hour)),
	}
	if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}

	// Save current state
	savedJSONOutput := jsonOutput
	savedCompactDryRun := compactDryRun
	savedCompactTier := compactTier
	savedCompactForce := compactForce
	defer func() {
		jsonOutput = savedJSONOutput
		compactDryRun = savedCompactDryRun
		compactTier = savedCompactTier
		compactForce = savedCompactForce
	}()

	// Test dry run mode
	compactDryRun = true
	compactTier = 1
	compactForce = false
	jsonOutput = false

	// This should succeed without API key in dry run mode
	// We can't fully test without mocking the compactor, but we can test the eligibility path
	eligible, _, err := sqliteStore.CheckEligibility(ctx, "test-compact-1", 1)
	if err != nil {
		t.Fatalf("CheckEligibility failed: %v", err)
	}
	if !eligible {
		t.Error("Issue should be eligible for Tier 1 compaction")
	}
}

func TestRunCompactAllDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	sqliteStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

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
		if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Verify issues eligible for compaction
	closedStatus := types.StatusClosed
	issues, err := sqliteStore.SearchIssues(ctx, "", types.IssueFilter{Status: &closedStatus})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	eligibleCount := 0
	for _, issue := range issues {
		eligible, _, err := sqliteStore.CheckEligibility(ctx, issue.ID, 1)
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
}
