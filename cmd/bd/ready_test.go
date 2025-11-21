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

	t.Run("ReadyWorkInProgress", func(t *testing.T) {
		// Create in-progress issue (should be in ready work)
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

		// Test that in-progress shows up in ready work
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
			t.Error("In-progress issue should appear in ready work")
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
