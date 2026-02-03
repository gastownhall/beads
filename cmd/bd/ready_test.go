package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestReadySuite(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("ReadyWork", func(t *testing.T) {
		// Create issues with different states
		issues := []*types.Issue{
			{
				ID:        "test-1",
				Title:     "Ready task 1",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-2",
				Title:     "Ready task 2",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-3",
				Title:     "Blocked task",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-blocker",
				Title:     "Blocking task",
				Status:    types.StatusOpen,
				Priority:  0,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-closed",
				Title:     "Closed task",
				Status:    types.StatusClosed,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
				ClosedAt:  ptrTime(time.Now()),
			},
		}

		for _, issue := range issues {
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Add dependency: test-3 depends on test-blocker
		dep := &types.Dependency{
			IssueID:     "test-3",
			DependsOnID: "test-blocker",
			Type:        types.DepBlocks,
			CreatedAt:   time.Now(),
		}
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatal(err)
		}

		// Test basic ready work
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork failed: %v", err)
		}

		// Should have test-1, test-2, test-blocker (not test-3 because it's blocked, not test-closed because it's closed)
		if len(ready) < 3 {
			t.Errorf("Expected at least 3 ready issues, got %d", len(ready))
		}

		// Check that test-3 is NOT in ready work
		for _, issue := range ready {
			if issue.ID == "test-3" {
				t.Error("test-3 should not be in ready work (it's blocked)")
			}
			if issue.ID == "test-closed" {
				t.Error("test-closed should not be in ready work (it's closed)")
			}
		}

		// Test with priority filter
		priority1 := 1
		readyP1, err := s.GetReadyWork(ctx, types.WorkFilter{
			Priority: &priority1,
		})
		if err != nil {
			t.Fatalf("GetReadyWork with priority filter failed: %v", err)
		}

		// Should only have priority 1 issues
		for _, issue := range readyP1 {
			if issue.Priority != 1 {
				t.Errorf("Expected priority 1, got %d for issue %s", issue.Priority, issue.ID)
			}
		}

		// Test with limit
		readyLimited, err := s.GetReadyWork(ctx, types.WorkFilter{
			Limit: 1,
		})
		if err != nil {
			t.Fatalf("GetReadyWork with limit failed: %v", err)
		}

		if len(readyLimited) > 1 {
			t.Errorf("Expected at most 1 issue with limit=1, got %d", len(readyLimited))
		}
	})

	t.Run("ReadyWorkWithAssignee", func(t *testing.T) {
		// Create issues with different assignees
		issues := []*types.Issue{
			{
				ID:        "test-alice",
				Title:     "Alice's task",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				Assignee:  "alice",
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-bob",
				Title:     "Bob's task",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				Assignee:  "bob",
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-unassigned",
				Title:     "Unassigned task",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
		}

		for _, issue := range issues {
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Test filtering by assignee
		alice := "alice"
		readyAlice, err := s.GetReadyWork(ctx, types.WorkFilter{
			Assignee: &alice,
		})
		if err != nil {
			t.Fatalf("GetReadyWork with assignee filter failed: %v", err)
		}

		if len(readyAlice) != 1 {
			t.Errorf("Expected 1 issue for alice, got %d", len(readyAlice))
		}

		if len(readyAlice) > 0 && readyAlice[0].Assignee != "alice" {
			t.Errorf("Expected assignee='alice', got %q", readyAlice[0].Assignee)
		}
	})

	t.Run("ReadyWorkUnassigned", func(t *testing.T) {
		// Test filtering for unassigned issues
		readyUnassigned, err := s.GetReadyWork(ctx, types.WorkFilter{
			Unassigned: true,
		})
		if err != nil {
			t.Fatalf("GetReadyWork with unassigned filter failed: %v", err)
		}

		// All returned issues should have no assignee
		for _, issue := range readyUnassigned {
			if issue.Assignee != "" {
				t.Errorf("Expected empty assignee, got %q for issue %s", issue.Assignee, issue.ID)
			}
		}

		// Should include test-unassigned from previous test
		found := false
		for _, issue := range readyUnassigned {
			if issue.ID == "test-unassigned" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected to find test-unassigned in unassigned results")
		}
	})

	t.Run("ReadyWorkInProgressWithEmptyFilter", func(t *testing.T) {
		// Create in-progress issue
		// When no status filter is set, GetReadyWork returns both open and in_progress
		// This tests the storage layer's default behavior (backwards compatibility)
		issue := &types.Issue{
			ID:        "test-wip",
			Title:     "Work in progress",
			Status:    types.StatusInProgress,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}

		// Test that in-progress shows up when filter.Status is empty (storage default)
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork failed: %v", err)
		}

		found := false
		for _, i := range ready {
			if i.ID == "test-wip" {
				found = true
				break
			}
		}

		if !found {
			t.Error("In-progress issue should appear when filter.Status is empty")
		}
	})

	t.Run("ReadyWorkExcludesInProgressWithOpenFilter", func(t *testing.T) {
		// When Status="open" is explicitly set, in_progress issues should NOT appear
		// This is the behavior used by 'bd ready' command to show only claimable work
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{
			Status: "open",
		})
		if err != nil {
			t.Fatalf("GetReadyWork with Status=open failed: %v", err)
		}

		for _, i := range ready {
			if i.ID == "test-wip" {
				t.Error("In-progress issue should NOT appear when filter.Status='open'")
			}
		}
	})
}

func TestReadyCommandInit(t *testing.T) {
	if readyCmd == nil {
		t.Fatal("readyCmd should be initialized")
	}

	if readyCmd.Use != "ready" {
		t.Errorf("Expected Use='ready', got %q", readyCmd.Use)
	}

	if len(readyCmd.Short) == 0 {
		t.Error("readyCmd should have Short description")
	}
}

// GH#820: Tests for defer_until filtering in ready work
func TestReadyWorkDeferUntil(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Create issues with different defer_until values
	futureDefer := time.Now().Add(24 * time.Hour) // Deferred to future
	pastDefer := time.Now().Add(-1 * time.Hour)   // Deferred to past (should be visible)

	issues := []*types.Issue{
		{
			ID:        "test-future-defer",
			Title:     "Future deferred task",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			DeferUntil: &futureDefer,
			CreatedAt: time.Now(),
		},
		{
			ID:        "test-past-defer",
			Title:     "Past deferred task",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			DeferUntil: &pastDefer,
			CreatedAt: time.Now(),
		},
		{
			ID:        "test-no-defer",
			Title:     "Normal task (no defer)",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("ExcludesFutureDeferredByDefault", func(t *testing.T) {
		// Default behavior: exclude issues with future defer_until
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork failed: %v", err)
		}

		// Should NOT include test-future-defer
		for _, issue := range ready {
			if issue.ID == "test-future-defer" {
				t.Error("Future deferred issue should not appear in ready work by default")
			}
		}

		// Should include test-past-defer and test-no-defer
		foundPast := false
		foundNoDefer := false
		for _, issue := range ready {
			if issue.ID == "test-past-defer" {
				foundPast = true
			}
			if issue.ID == "test-no-defer" {
				foundNoDefer = true
			}
		}

		if !foundPast {
			t.Error("Past deferred issue should appear in ready work")
		}
		if !foundNoDefer {
			t.Error("Issue without defer should appear in ready work")
		}
	})

	t.Run("IncludeDeferredShowsAll", func(t *testing.T) {
		// With IncludeDeferred: show all issues including future deferred
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{
			IncludeDeferred: true,
		})
		if err != nil {
			t.Fatalf("GetReadyWork with IncludeDeferred failed: %v", err)
		}

		// Should include test-future-defer
		foundFuture := false
		for _, issue := range ready {
			if issue.ID == "test-future-defer" {
				foundFuture = true
				break
			}
		}

		if !foundFuture {
			t.Error("Future deferred issue should appear when IncludeDeferred=true")
		}
	})
}

func TestReadyWorkUnassigned(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Create issues with different assignees
	issues := []*types.Issue{
		{
			ID:        "test-unassigned-1",
			Title:     "Unassigned task 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Assignee:  "",
			CreatedAt: time.Now(),
		},
		{
			ID:        "test-unassigned-2",
			Title:     "Unassigned task 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		},
		{
			ID:        "test-assigned-alice",
			Title:     "Alice's task",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Assignee:  "alice",
			CreatedAt: time.Now(),
		},
		{
			ID:        "test-assigned-bob",
			Title:     "Bob's task",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Assignee:  "bob",
			CreatedAt: time.Now(),
		},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Test filtering by --unassigned
	readyUnassigned, err := s.GetReadyWork(ctx, types.WorkFilter{
		Unassigned: true,
	})
	if err != nil {
		t.Fatalf("GetReadyWork with Unassigned filter failed: %v", err)
	}

	// Should only have unassigned issues
	if len(readyUnassigned) != 2 {
		t.Errorf("Expected 2 unassigned issues, got %d", len(readyUnassigned))
	}

	for _, issue := range readyUnassigned {
		if issue.Assignee != "" {
			t.Errorf("Expected no assignee, got %q for issue %s", issue.Assignee, issue.ID)
		}
	}

	// Test that Unassigned takes precedence over Assignee filter
	alice := "alice"
	readyConflict, err := s.GetReadyWork(ctx, types.WorkFilter{
		Unassigned: true,
		Assignee:   &alice,
	})
	if err != nil {
		t.Fatalf("GetReadyWork with conflicting filters failed: %v", err)
	}

	// Unassigned should win, returning only unassigned issues
	for _, issue := range readyConflict {
		if issue.Assignee != "" {
			t.Errorf("Unassigned should override Assignee filter, got %q for issue %s", issue.Assignee, issue.ID)
		}
	}
}
