//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestStaleSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Create ALL test data up front — one DB for all stale subtests.
	issues := []*types.Issue{
		// Basic stale detection
		{ID: "test-stale-1", Title: "Very stale issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-stale-2", Title: "Stale in-progress", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-recent", Title: "Recently updated", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-closed", Title: "Closed issue", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
		// Status filter
		{ID: "test-sf-open", Title: "Stale open", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-sf-inprog", Title: "Stale in-progress", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-sf-blocked", Title: "Stale blocked", Status: types.StatusBlocked, Priority: 1, IssueType: types.TypeTask},
		// Limit test
		{ID: "test-stale-limit-1", Title: "Stale limit 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-stale-limit-2", Title: "Stale limit 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-stale-limit-3", Title: "Stale limit 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-stale-limit-4", Title: "Stale limit 4", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-stale-limit-5", Title: "Stale limit 5", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		// Recent-only (for "no stale" check with high threshold)
		{ID: "test-recent-only", Title: "Recent issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		// Threshold comparison
		{ID: "test-20-days", Title: "20 days stale", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "test-50-days", Title: "50 days stale", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Set timestamps via direct SQL (CreateIssue sets updated_at to now).
	db := s.UnderlyingDB()

	// 40 days old
	_, err := db.ExecContext(ctx,
		"UPDATE issues SET updated_at = DATE_SUB(NOW(), INTERVAL 40 DAY) WHERE id IN (?, ?, ?, ?, ?)",
		"test-stale-1", "test-stale-2", "test-sf-open", "test-sf-inprog", "test-sf-blocked")
	if err != nil {
		t.Fatal(err)
	}

	// 10 days old
	_, err = db.ExecContext(ctx,
		"UPDATE issues SET updated_at = DATE_SUB(NOW(), INTERVAL 10 DAY) WHERE id IN (?, ?)",
		"test-recent", "test-recent-only")
	if err != nil {
		t.Fatal(err)
	}

	// Limit issues: ~40 days old with slight variation
	for i := 1; i <= 5; i++ {
		id := "test-stale-limit-" + strconv.Itoa(i)
		_, err := db.ExecContext(ctx,
			"UPDATE issues SET updated_at = DATE_SUB(NOW(), INTERVAL (40*24 - ?) HOUR) WHERE id = ?", i, id)
		if err != nil {
			t.Fatal(err)
		}
	}

	// 20 days old
	_, err = db.ExecContext(ctx,
		"UPDATE issues SET updated_at = DATE_SUB(NOW(), INTERVAL 20 DAY) WHERE id = ?", "test-20-days")
	if err != nil {
		t.Fatal(err)
	}

	// 50 days old
	_, err = db.ExecContext(ctx,
		"UPDATE issues SET updated_at = DATE_SUB(NOW(), INTERVAL 50 DAY) WHERE id = ?", "test-50-days")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("BasicStaleDetection", func(t *testing.T) {
		stale, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 30, Limit: 50})
		if err != nil {
			t.Fatalf("GetStaleIssues failed: %v", err)
		}

		staleIDs := make(map[string]bool)
		for _, issue := range stale {
			staleIDs[issue.ID] = true
		}

		// 40+ day-old non-closed issues should be stale (blocked status may be excluded by default)
		for _, id := range []string{"test-stale-1", "test-stale-2", "test-sf-open", "test-sf-inprog", "test-50-days"} {
			if !staleIDs[id] {
				t.Errorf("Expected %s to be stale (30-day threshold)", id)
			}
		}
		for i := 1; i <= 5; i++ {
			if !staleIDs["test-stale-limit-"+strconv.Itoa(i)] {
				t.Errorf("Expected test-stale-limit-%d to be stale", i)
			}
		}

		// Recent, closed, and <30 day issues should NOT be stale
		for _, id := range []string{"test-recent", "test-closed", "test-recent-only", "test-20-days"} {
			if staleIDs[id] {
				t.Errorf("Expected %s to NOT be stale (30-day threshold)", id)
			}
		}

		// Verify no closed issues in results
		for _, issue := range stale {
			if issue.Status == types.StatusClosed {
				t.Error("Closed issues should not appear in stale results")
			}
		}

		// Verify sorted by updated_at ascending (oldest first)
		for i := 0; i < len(stale)-1; i++ {
			if stale[i].UpdatedAt.After(stale[i+1].UpdatedAt) {
				t.Error("Stale issues should be sorted by updated_at ascending (oldest first)")
			}
		}
	})

	t.Run("StatusFilter", func(t *testing.T) {
		// Filter: only in_progress
		stale, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 30, Status: "in_progress", Limit: 50})
		if err != nil {
			t.Fatalf("GetStaleIssues with status=in_progress failed: %v", err)
		}

		staleIDs := make(map[string]bool)
		for _, issue := range stale {
			staleIDs[issue.ID] = true
			if issue.Status != types.StatusInProgress {
				t.Errorf("Expected status=in_progress, got %s for %s", issue.Status, issue.ID)
			}
		}
		if !staleIDs["test-stale-2"] {
			t.Error("Expected test-stale-2 in in_progress stale results")
		}
		if !staleIDs["test-sf-inprog"] {
			t.Error("Expected test-sf-inprog in in_progress stale results")
		}

		// Filter: only open
		staleOpen, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 30, Status: "open", Limit: 50})
		if err != nil {
			t.Fatalf("GetStaleIssues with status=open failed: %v", err)
		}

		for _, issue := range staleOpen {
			if issue.Status != types.StatusOpen {
				t.Errorf("Expected status=open, got %s for %s", issue.Status, issue.ID)
			}
		}

		openIDs := make(map[string]bool)
		for _, issue := range staleOpen {
			openIDs[issue.ID] = true
		}
		if !openIDs["test-stale-1"] {
			t.Error("Expected test-stale-1 in open stale results")
		}
		if !openIDs["test-sf-open"] {
			t.Error("Expected test-sf-open in open stale results")
		}
	})

	t.Run("WithLimit", func(t *testing.T) {
		stale, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 30, Limit: 2})
		if err != nil {
			t.Fatalf("GetStaleIssues with limit failed: %v", err)
		}
		if len(stale) != 2 {
			t.Errorf("Expected 2 issues with limit=2, got %d", len(stale))
		}
	})

	t.Run("NoStaleWithHighThreshold", func(t *testing.T) {
		// No issue is 1000 days old, so this should return empty
		stale, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 1000, Limit: 50})
		if err != nil {
			t.Fatalf("GetStaleIssues failed: %v", err)
		}
		if len(stale) != 0 {
			t.Errorf("Expected 0 stale issues with 1000-day threshold, got %d", len(stale))
		}
	})

	t.Run("DifferentDaysThreshold", func(t *testing.T) {
		// Helper to collect IDs from stale results
		collectIDs := func(issues []*types.Issue) map[string]bool {
			m := make(map[string]bool)
			for _, i := range issues {
				m[i.ID] = true
			}
			return m
		}

		// 10-day threshold: should include 20-day and 50-day issues
		stale10, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 10, Limit: 50})
		if err != nil {
			t.Fatalf("GetStaleIssues(10 days) failed: %v", err)
		}
		ids10 := collectIDs(stale10)
		if !ids10["test-20-days"] {
			t.Error("Expected test-20-days in 10-day stale results")
		}
		if !ids10["test-50-days"] {
			t.Error("Expected test-50-days in 10-day stale results")
		}

		// 30-day threshold: should NOT include test-20-days, SHOULD include test-50-days
		stale30, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 30, Limit: 50})
		if err != nil {
			t.Fatalf("GetStaleIssues(30 days) failed: %v", err)
		}
		ids30 := collectIDs(stale30)
		if ids30["test-20-days"] {
			t.Error("test-20-days should NOT be in 30-day stale results")
		}
		if !ids30["test-50-days"] {
			t.Error("Expected test-50-days in 30-day stale results")
		}

		// 60-day threshold: should include neither 20-day nor 50-day issues
		stale60, err := s.GetStaleIssues(ctx, types.StaleFilter{Days: 60, Limit: 50})
		if err != nil {
			t.Fatalf("GetStaleIssues(60 days) failed: %v", err)
		}
		ids60 := collectIDs(stale60)
		if ids60["test-20-days"] {
			t.Error("test-20-days should NOT be in 60-day stale results")
		}
		if ids60["test-50-days"] {
			t.Error("test-50-days should NOT be in 60-day stale results")
		}
	})
}

func TestStaleCommandInit(t *testing.T) {
	t.Parallel()
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
