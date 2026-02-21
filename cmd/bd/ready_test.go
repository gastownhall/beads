//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestReadySuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// ========== Shared data setup ==========
	// All sub-tests share one DB. IDs are unique across all sub-tests.

	// --- Core ready work data ---
	coreIssues := []*types.Issue{
		{ID: "test-1", Title: "Ready task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-2", Title: "Ready task 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-3", Title: "Blocked task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-blocker", Title: "Blocking task", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-closed", Title: "Closed task", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now(), ClosedAt: ptrTime(time.Now())},
	}
	for _, issue := range coreIssues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}
	// test-3 depends on test-blocker
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID: "test-3", DependsOnID: "test-blocker", Type: types.DepBlocks, CreatedAt: time.Now(),
	}, "test"); err != nil {
		t.Fatal(err)
	}

	// --- Assignee data ---
	assigneeIssues := []*types.Issue{
		{ID: "test-alice", Title: "Alice's task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "alice", CreatedAt: time.Now()},
		{ID: "test-bob", Title: "Bob's task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "bob", CreatedAt: time.Now()},
		{ID: "test-unassigned", Title: "Unassigned task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
	}
	for _, issue := range assigneeIssues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// --- In-progress data ---
	if err := s.CreateIssue(ctx, &types.Issue{
		ID: "test-wip", Title: "Work in progress", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now(),
	}, "test"); err != nil {
		t.Fatal(err)
	}

	// --- Closed-blocker data ---
	closedBlockerIssues := []*types.Issue{
		{ID: "test-closed-blocker-1", Title: "Closed blocker 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-closed-blocker-2", Title: "Closed blocker 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-open-blocker", Title: "Open blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-ready-via-closed-blockers", Title: "Ready when all blockers are closed", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-still-blocked", Title: "Still blocked by open blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
	}
	for _, issue := range closedBlockerIssues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}
	closedBlockerDeps := []*types.Dependency{
		{IssueID: "test-ready-via-closed-blockers", DependsOnID: "test-closed-blocker-1", Type: types.DepBlocks, CreatedAt: time.Now()},
		{IssueID: "test-ready-via-closed-blockers", DependsOnID: "test-closed-blocker-2", Type: types.DepBlocks, CreatedAt: time.Now()},
		{IssueID: "test-still-blocked", DependsOnID: "test-closed-blocker-1", Type: types.DepBlocks, CreatedAt: time.Now()},
		{IssueID: "test-still-blocked", DependsOnID: "test-open-blocker", Type: types.DepBlocks, CreatedAt: time.Now()},
	}
	for _, dep := range closedBlockerDeps {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseIssue(ctx, "test-closed-blocker-1", "completed", "test", "session-ready-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseIssue(ctx, "test-closed-blocker-2", "completed", "test", "session-ready-2"); err != nil {
		t.Fatal(err)
	}

	// --- Epic/parent-child data (for buildParentEpicMap) ---
	epicIssues := []*types.Issue{
		{ID: "test-epic", Title: "Auth Overhaul", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: time.Now()},
		{ID: "test-parent-task", Title: "Parent Task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-child-1", Title: "Implement login", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-child-2", Title: "Subtask of non-epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-orphan", Title: "Standalone task", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask, CreatedAt: time.Now()},
	}
	for _, issue := range epicIssues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}
	epicDeps := []*types.Dependency{
		{IssueID: "test-child-1", DependsOnID: "test-epic", Type: types.DepParentChild, CreatedAt: time.Now()},
		{IssueID: "test-child-2", DependsOnID: "test-parent-task", Type: types.DepParentChild, CreatedAt: time.Now()},
	}
	for _, dep := range epicDeps {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// --- Defer data ---
	futureDefer := time.Now().Add(24 * time.Hour)
	pastDefer := time.Now().Add(-1 * time.Hour)
	deferIssues := []*types.Issue{
		{ID: "test-future-defer", Title: "Future deferred task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, DeferUntil: &futureDefer, CreatedAt: time.Now()},
		{ID: "test-past-defer", Title: "Past deferred task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, DeferUntil: &pastDefer, CreatedAt: time.Now()},
		{ID: "test-no-defer", Title: "Normal task (no defer)", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()},
	}
	for _, issue := range deferIssues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// --- Unassigned-specific data ---
	unassignedIssues := []*types.Issue{
		{ID: "test-unassigned-1", Title: "Unassigned task 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "", CreatedAt: time.Now()},
		{ID: "test-unassigned-2", Title: "Unassigned task 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now()},
		{ID: "test-assigned-alice", Title: "Alice's task 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "alice", CreatedAt: time.Now()},
		{ID: "test-assigned-bob", Title: "Bob's task 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "bob", CreatedAt: time.Now()},
	}
	for _, issue := range unassignedIssues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// ========== Sub-tests ==========

	t.Run("ReadyWork", func(t *testing.T) {
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork failed: %v", err)
		}

		readyIDs := make(map[string]bool)
		for _, issue := range ready {
			readyIDs[issue.ID] = true
		}

		// test-1, test-2, test-blocker should be in ready work
		for _, id := range []string{"test-1", "test-2", "test-blocker"} {
			if !readyIDs[id] {
				t.Errorf("Expected %s in ready work", id)
			}
		}

		// test-3 (blocked) and test-closed should NOT be in ready work
		if readyIDs["test-3"] {
			t.Error("test-3 should not be in ready work (it's blocked)")
		}
		if readyIDs["test-closed"] {
			t.Error("test-closed should not be in ready work (it's closed)")
		}

		// Priority filter
		priority1 := 1
		readyP1, err := s.GetReadyWork(ctx, types.WorkFilter{Priority: &priority1})
		if err != nil {
			t.Fatalf("GetReadyWork with priority filter failed: %v", err)
		}
		for _, issue := range readyP1 {
			if issue.Priority != 1 {
				t.Errorf("Expected priority 1, got %d for issue %s", issue.Priority, issue.ID)
			}
		}

		// Limit
		readyLimited, err := s.GetReadyWork(ctx, types.WorkFilter{Limit: 1})
		if err != nil {
			t.Fatalf("GetReadyWork with limit failed: %v", err)
		}
		if len(readyLimited) > 1 {
			t.Errorf("Expected at most 1 issue with limit=1, got %d", len(readyLimited))
		}
	})

	t.Run("ReadyWorkWithAssignee", func(t *testing.T) {
		alice := "alice"
		readyAlice, err := s.GetReadyWork(ctx, types.WorkFilter{Assignee: &alice})
		if err != nil {
			t.Fatalf("GetReadyWork with assignee filter failed: %v", err)
		}

		// All returned issues should be assigned to alice
		for _, issue := range readyAlice {
			if issue.Assignee != "alice" {
				t.Errorf("Expected assignee='alice', got %q for %s", issue.Assignee, issue.ID)
			}
		}

		// Should include test-alice
		found := false
		for _, issue := range readyAlice {
			if issue.ID == "test-alice" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected test-alice in assignee-filtered results")
		}
	})

	t.Run("ReadyWorkUnassignedFilter", func(t *testing.T) {
		readyUnassigned, err := s.GetReadyWork(ctx, types.WorkFilter{Unassigned: true})
		if err != nil {
			t.Fatalf("GetReadyWork with unassigned filter failed: %v", err)
		}

		// All returned issues should have no assignee
		for _, issue := range readyUnassigned {
			if issue.Assignee != "" {
				t.Errorf("Expected empty assignee, got %q for issue %s", issue.Assignee, issue.ID)
			}
		}

		// Should include test-unassigned
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
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{Status: "open"})
		if err != nil {
			t.Fatalf("GetReadyWork with Status=open failed: %v", err)
		}

		for _, i := range ready {
			if i.ID == "test-wip" {
				t.Error("In-progress issue should NOT appear when filter.Status='open'")
			}
		}
	})

	t.Run("ReadyWorkIncludesIssuesWhoseBlockersAreClosed", func(t *testing.T) {
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{Status: "open"})
		if err != nil {
			t.Fatalf("GetReadyWork with Status=open failed: %v", err)
		}

		foundReadyViaClosed := false
		foundStillBlocked := false
		for _, issue := range ready {
			if issue.ID == "test-ready-via-closed-blockers" {
				foundReadyViaClosed = true
			}
			if issue.ID == "test-still-blocked" {
				foundStillBlocked = true
			}
		}

		if !foundReadyViaClosed {
			t.Error("Issue with only closed blockers should be in ready work")
		}
		if foundStillBlocked {
			t.Error("Issue with any open blocker should not be in ready work")
		}
	})

	// --- buildParentEpicMap tests (merged from TestBuildParentEpicMap) ---

	t.Run("BuildParentEpicMap_MapsChildToEpicParentOnly", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "test-child-1", Title: "Implement login", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "test-child-2", Title: "Subtask of non-epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "test-orphan", Title: "Standalone task", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask},
		}
		result := buildParentEpicMap(ctx, s, issues)

		if result["test-child-1"] != "Auth Overhaul" {
			t.Errorf("Expected test-child-1 to map to 'Auth Overhaul', got %q", result["test-child-1"])
		}
		if _, ok := result["test-child-2"]; ok {
			t.Errorf("test-child-2 should not be in map (parent is not an epic), got %q", result["test-child-2"])
		}
		if _, ok := result["test-orphan"]; ok {
			t.Errorf("test-orphan should not be in map (no parent)")
		}
	})

	t.Run("BuildParentEpicMap_EmptyIssuesReturnsNil", func(t *testing.T) {
		result := buildParentEpicMap(ctx, s, nil)
		if result != nil {
			t.Errorf("Expected nil for empty issues, got %v", result)
		}
	})

	t.Run("BuildParentEpicMap_NoParentDepsReturnsNil", func(t *testing.T) {
		orphan := &types.Issue{ID: "test-orphan", Title: "Standalone task", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}
		result := buildParentEpicMap(ctx, s, []*types.Issue{orphan})
		if result != nil {
			t.Errorf("Expected nil when no parent deps exist, got %v", result)
		}
	})

	// --- Defer tests (merged from TestReadyWorkDeferUntil) ---

	t.Run("DeferUntil_ExcludesFutureDeferredByDefault", func(t *testing.T) {
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork failed: %v", err)
		}

		for _, issue := range ready {
			if issue.ID == "test-future-defer" {
				t.Error("Future deferred issue should not appear in ready work by default")
			}
		}

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

	t.Run("DeferUntil_IncludeDeferredShowsAll", func(t *testing.T) {
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{IncludeDeferred: true})
		if err != nil {
			t.Fatalf("GetReadyWork with IncludeDeferred failed: %v", err)
		}

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

	// --- Unassigned tests (merged from TestReadyWorkUnassigned) ---

	t.Run("Unassigned_FiltersCorrectly", func(t *testing.T) {
		readyUnassigned, err := s.GetReadyWork(ctx, types.WorkFilter{Unassigned: true})
		if err != nil {
			t.Fatalf("GetReadyWork with Unassigned filter failed: %v", err)
		}

		// All returned issues should have no assignee
		for _, issue := range readyUnassigned {
			if issue.Assignee != "" {
				t.Errorf("Expected no assignee, got %q for issue %s", issue.Assignee, issue.ID)
			}
		}

		// Should include test-unassigned-1 and test-unassigned-2
		unassignedIDs := make(map[string]bool)
		for _, issue := range readyUnassigned {
			unassignedIDs[issue.ID] = true
		}
		if !unassignedIDs["test-unassigned-1"] {
			t.Error("Expected test-unassigned-1 in unassigned results")
		}
		if !unassignedIDs["test-unassigned-2"] {
			t.Error("Expected test-unassigned-2 in unassigned results")
		}
	})

	t.Run("Unassigned_TakesPrecedenceOverAssignee", func(t *testing.T) {
		alice := "alice"
		readyConflict, err := s.GetReadyWork(ctx, types.WorkFilter{Unassigned: true, Assignee: &alice})
		if err != nil {
			t.Fatalf("GetReadyWork with conflicting filters failed: %v", err)
		}

		// Unassigned should win, returning only unassigned issues
		for _, issue := range readyConflict {
			if issue.Assignee != "" {
				t.Errorf("Unassigned should override Assignee filter, got %q for issue %s", issue.Assignee, issue.ID)
			}
		}
	})
}

func TestReadyCommandInit(t *testing.T) {
	t.Parallel()
	if readyCmd == nil {
		t.Fatal("readyCmd should be initialized")
	}

	if readyCmd.Use != "ready" {
		t.Errorf("Expected Use='ready', got %q", readyCmd.Use)
	}

	if len(readyCmd.Short) == 0 {
		t.Error("readyCmd should have Short description")
	}

	// Verify --pretty defaults to true
	prettyFlag := readyCmd.Flags().Lookup("pretty")
	if prettyFlag == nil {
		t.Fatal("--pretty flag should exist")
	}
	if prettyFlag.DefValue != "true" {
		t.Errorf("--pretty default should be 'true', got %q", prettyFlag.DefValue)
	}

	// Verify --plain flag exists and defaults to false
	plainFlag := readyCmd.Flags().Lookup("plain")
	if plainFlag == nil {
		t.Fatal("--plain flag should exist")
	}
	if plainFlag.DefValue != "false" {
		t.Errorf("--plain default should be 'false', got %q", plainFlag.DefValue)
	}

	// Verify --sort defaults to "priority"
	sortFlag := readyCmd.Flags().Lookup("sort")
	if sortFlag == nil {
		t.Fatal("--sort flag should exist")
	}
	if sortFlag.DefValue != "priority" {
		t.Errorf("--sort default should be 'priority', got %q", sortFlag.DefValue)
	}
}
