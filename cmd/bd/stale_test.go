package main

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestStaleIssues(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	now := time.Now()
	oldTime := now.Add(-40 * 24 * time.Hour) // 40 days ago
	recentTime := now.Add(-10 * 24 * time.Hour) // 10 days ago

	// Create issues with different update times
	issues := []*types.Issue{
		{
			ID:        "test-stale-1",
			Title:     "Very stale issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: oldTime,
		},
		{
			ID:        "test-stale-2",
			Title:     "Stale in-progress",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: oldTime,
		},
		{
			ID:        "test-recent",
			Title:     "Recently updated",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: recentTime,
		},
		{
			ID:        "test-closed",
			Title:     "Closed issue",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: oldTime,
			ClosedAt:  ptrTime(oldTime),
		},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Update timestamps directly in DB (CreateIssue sets updated_at to now)
	// Use datetime() function to compute old timestamps
	db := s.UnderlyingDB()
	_, err := db.ExecContext(ctx, "UPDATE issues SET updated_at = datetime('now', '-40 days') WHERE id IN (?, ?)", "test-stale-1", "test-stale-2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, "UPDATE issues SET updated_at = datetime('now', '-10 days') WHERE id = ?", "test-recent")
	if err != nil {
		t.Fatal(err)
	}

	// Test basic stale detection (30 days)
	stale, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:  30,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues failed: %v", err)
	}

	// Should have test-stale-1 and test-stale-2 (not test-recent or test-closed)
	if len(stale) != 2 {
		t.Errorf("Expected 2 stale issues, got %d", len(stale))
	}

	// Verify closed issues are excluded
	for _, issue := range stale {
		if issue.Status == types.StatusClosed {
			t.Error("Closed issues should not appear in stale results")
		}
		if issue.ID == "test-closed" {
			t.Error("test-closed should not be in stale results")
		}
		if issue.ID == "test-recent" {
			t.Error("test-recent should not be in stale results (updated 10 days ago)")
		}
	}

	// Verify issues are sorted by updated_at (oldest first)
	for i := 0; i < len(stale)-1; i++ {
		if stale[i].UpdatedAt.After(stale[i+1].UpdatedAt) {
			t.Error("Stale issues should be sorted by updated_at ascending (oldest first)")
		}
	}
}

func TestStaleIssuesWithStatusFilter(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	oldTime := time.Now().Add(-40 * 24 * time.Hour)

	// Create stale issues with different statuses
	issues := []*types.Issue{
		{
			ID:        "test-open",
			Title:     "Stale open",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: oldTime,
		},
		{
			ID:        "test-in-progress",
			Title:     "Stale in-progress",
			Status:    types.StatusInProgress,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: oldTime,
		},
		{
			ID:        "test-blocked",
			Title:     "Stale blocked",
			Status:    types.StatusBlocked,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: oldTime,
		},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Update timestamps directly in DB using datetime() function
	db := s.UnderlyingDB()
	_, err := db.ExecContext(ctx, "UPDATE issues SET updated_at = datetime('now', '-40 days') WHERE id IN (?, ?, ?)",
		"test-open", "test-in-progress", "test-blocked")
	if err != nil {
		t.Fatal(err)
	}

	// Test status filter: only in_progress
	stale, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:   30,
		Status: "in_progress",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues with status filter failed: %v", err)
	}

	if len(stale) != 1 {
		t.Errorf("Expected 1 in_progress stale issue, got %d", len(stale))
	}

	if len(stale) > 0 && stale[0].Status != types.StatusInProgress {
		t.Errorf("Expected status=in_progress, got %s", stale[0].Status)
	}

	// Test status filter: only open
	staleOpen, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:   30,
		Status: "open",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues with status=open failed: %v", err)
	}

	if len(staleOpen) != 1 {
		t.Errorf("Expected 1 open stale issue, got %d", len(staleOpen))
	}

	if len(staleOpen) > 0 && staleOpen[0].Status != types.StatusOpen {
		t.Errorf("Expected status=open, got %s", staleOpen[0].Status)
	}
}

func TestStaleIssuesWithLimit(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	oldTime := time.Now().Add(-40 * 24 * time.Hour)

	// Create multiple stale issues
	for i := 1; i <= 5; i++ {
		updatedAt := oldTime.Add(time.Duration(i) * time.Hour) // Slightly different times for sorting
		issue := &types.Issue{
			ID:        "test-stale-limit-" + strconv.Itoa(i),
			Title:     "Stale issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: updatedAt,
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Update timestamps directly in DB using datetime() function
	db := s.UnderlyingDB()
	for i := 1; i <= 5; i++ {
		id := "test-stale-limit-" + strconv.Itoa(i)
		// Make each slightly different (40 days ago + i hours)
		_, err := db.ExecContext(ctx, "UPDATE issues SET updated_at = datetime('now', '-40 days', '+' || ? || ' hours') WHERE id = ?", i, id)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test with limit
	stale, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:  30,
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues with limit failed: %v", err)
	}

	if len(stale) != 2 {
		t.Errorf("Expected 2 issues with limit=2, got %d", len(stale))
	}
}

func TestStaleIssuesEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	recentTime := time.Now().Add(-10 * 24 * time.Hour)

	// Create only recent issues
	issue := &types.Issue{
		ID:        "test-recent-only",
		Title:     "Recent issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: recentTime,
		UpdatedAt: recentTime,
	}

	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}

	// Test stale detection with no stale issues
	stale, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:  30,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues failed: %v", err)
	}

	if len(stale) != 0 {
		t.Errorf("Expected 0 stale issues, got %d", len(stale))
	}
}

func TestStaleIssuesDifferentDaysThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	now := time.Now()
	time20DaysAgo := now.Add(-20 * 24 * time.Hour)
	time50DaysAgo := now.Add(-50 * 24 * time.Hour)

	issues := []*types.Issue{
		{
			ID:        "test-20-days",
			Title:     "20 days stale",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time20DaysAgo,
			UpdatedAt: time20DaysAgo,
		},
		{
			ID:        "test-50-days",
			Title:     "50 days stale",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time50DaysAgo,
			UpdatedAt: time50DaysAgo,
		},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Update timestamps directly in DB using datetime() function
	db := s.UnderlyingDB()
	_, err := db.ExecContext(ctx, "UPDATE issues SET updated_at = datetime('now', '-20 days') WHERE id = ?", "test-20-days")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, "UPDATE issues SET updated_at = datetime('now', '-50 days') WHERE id = ?", "test-50-days")
	if err != nil {
		t.Fatal(err)
	}

	// Test with 30 days threshold - should get both
	stale30, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:  30,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues(30 days) failed: %v", err)
	}

	if len(stale30) != 1 {
		t.Errorf("Expected 1 issue stale for 30+ days, got %d", len(stale30))
	}

	// Test with 10 days threshold - should get both
	stale10, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:  10,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues(10 days) failed: %v", err)
	}

	if len(stale10) != 2 {
		t.Errorf("Expected 2 issues stale for 10+ days, got %d", len(stale10))
	}

	// Test with 60 days threshold - should get only the 50-day old one
	stale60, err := s.GetStaleIssues(ctx, types.StaleFilter{
		Days:  60,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("GetStaleIssues(60 days) failed: %v", err)
	}

	if len(stale60) != 0 {
		t.Errorf("Expected 0 issues stale for 60+ days, got %d", len(stale60))
	}
}

func TestStaleCommandInit(t *testing.T) {
	if staleCmd == nil {
		t.Fatal("staleCmd should be initialized")
	}

	if staleCmd.Use != "stale" {
		t.Errorf("Expected Use='stale', got %q", staleCmd.Use)
	}

	if len(staleCmd.Short) == 0 {
		t.Error("staleCmd should have Short description")
	}

	// Check flags are defined
	flags := staleCmd.Flags()
	if flags.Lookup("days") == nil {
		t.Error("staleCmd should have --days flag")
	}
	if flags.Lookup("status") == nil {
		t.Error("staleCmd should have --status flag")
	}
	if flags.Lookup("limit") == nil {
		t.Error("staleCmd should have --limit flag")
	}
	// --json is inherited from rootCmd as a persistent flag
	if staleCmd.InheritedFlags().Lookup("json") == nil {
		t.Error("staleCmd should inherit --json flag from rootCmd")
	}
}
