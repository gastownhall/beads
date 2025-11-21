package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestEpicCommand(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	sqliteStore := newTestStore(t, testDB)
	ctx := context.Background()

	// Create an epic with children
	epic := &types.Issue{
		ID:          "test-epic-1",
		Title:       "Test Epic",
		Description: "Epic description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   time.Now(),
	}

	if err := sqliteStore.CreateIssue(ctx, epic, "test"); err != nil {
		t.Fatal(err)
	}

	// Create child tasks
	child1 := &types.Issue{
		Title:     "Child Task 1",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		ClosedAt:  ptrTime(time.Now()),
	}

	child2 := &types.Issue{
		Title:     "Child Task 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := sqliteStore.CreateIssue(ctx, child1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.CreateIssue(ctx, child2, "test"); err != nil {
		t.Fatal(err)
	}

	// Add parent-child dependencies
	dep1 := &types.Dependency{
		IssueID:     child1.ID,
		DependsOnID: epic.ID,
		Type:        types.DepParentChild,
	}
	dep2 := &types.Dependency{
		IssueID:     child2.ID,
		DependsOnID: epic.ID,
		Type:        types.DepParentChild,
	}

	if err := sqliteStore.AddDependency(ctx, dep1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.AddDependency(ctx, dep2, "test"); err != nil {
		t.Fatal(err)
	}

	// Test GetEpicsEligibleForClosure
	store = sqliteStore
	daemonClient = nil

	epics, err := sqliteStore.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
	}

	if len(epics) != 1 {
		t.Errorf("Expected 1 epic, got %d", len(epics))
	}

	if len(epics) > 0 {
		epicStatus := epics[0]
		if epicStatus.Epic.ID != "test-epic-1" {
			t.Errorf("Expected epic ID test-epic-1, got %s", epicStatus.Epic.ID)
		}
		if epicStatus.TotalChildren != 2 {
			t.Errorf("Expected 2 total children, got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 1 {
			t.Errorf("Expected 1 closed child, got %d", epicStatus.ClosedChildren)
		}
		if epicStatus.EligibleForClose {
			t.Error("Epic should not be eligible for close with open children")
		}
	}
}

func TestEpicCommandInit(t *testing.T) {
	if epicCmd == nil {
		t.Fatal("epicCmd should be initialized")
	}

	if epicCmd.Use != "epic" {
		t.Errorf("Expected Use='epic', got %q", epicCmd.Use)
	}

	// Check that subcommands exist
	var hasStatusCmd bool
	for _, cmd := range epicCmd.Commands() {
		if cmd.Use == "status" {
			hasStatusCmd = true
		}
	}

	if !hasStatusCmd {
		t.Error("epic command should have status subcommand")
	}
}

func TestEpicEligibleForClose(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	sqliteStore := newTestStore(t, testDB)
	ctx := context.Background()

	// Create an epic where all children are closed
	epic := &types.Issue{
		ID:          "test-epic-2",
		Title:       "Fully Completed Epic",
		Description: "Epic description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   time.Now(),
	}

	if err := sqliteStore.CreateIssue(ctx, epic, "test"); err != nil {
		t.Fatal(err)
	}

	// Create all closed children
	for i := 1; i <= 3; i++ {
		child := &types.Issue{
			Title:     fmt.Sprintf("Child Task %d", i),
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			ClosedAt:  ptrTime(time.Now()),
		}
		if err := sqliteStore.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatal(err)
		}

		// Add parent-child dependency
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		}
		if err := sqliteStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Test GetEpicsEligibleForClosure
	epics, err := sqliteStore.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
	}

	// Find our epic
	var epicStatus *types.EpicStatus
	for _, e := range epics {
		if e.Epic.ID == "test-epic-2" {
			epicStatus = e
			break
		}
	}

	if epicStatus == nil {
		t.Fatal("Epic test-epic-2 not found in results")
	}

	if epicStatus.TotalChildren != 3 {
		t.Errorf("Expected 3 total children, got %d", epicStatus.TotalChildren)
	}
	if epicStatus.ClosedChildren != 3 {
		t.Errorf("Expected 3 closed children, got %d", epicStatus.ClosedChildren)
	}
	if !epicStatus.EligibleForClose {
		t.Error("Epic should be eligible for close when all children are closed")
	}
}
