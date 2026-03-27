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

func TestEpicEligibleForCloseWithWispChildren(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	sqliteStore := newTestStore(t, testDB)
	ctx := context.Background()

	// Create an epic with one regular child and one wisp child.
	epic := &types.Issue{
		ID:          "test-epic-wisp",
		Title:       "Epic with wisp child",
		Description: "Tests that wisp children are counted for closure eligibility",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   time.Now(),
	}
	if err := sqliteStore.CreateIssue(ctx, epic, "test"); err != nil {
		t.Fatal(err)
	}

	// Regular child (closed)
	regularChild := &types.Issue{
		Title:     "Regular child",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		ClosedAt:  ptrTime(time.Now()),
	}
	if err := sqliteStore.CreateIssue(ctx, regularChild, "test"); err != nil {
		t.Fatal(err)
	}

	// Wisp child (still open) — stored in wisps table
	wispChild := &types.Issue{
		Title:     "Wisp child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
		CreatedAt: time.Now(),
	}
	if err := sqliteStore.CreateIssue(ctx, wispChild, "test"); err != nil {
		t.Fatal(err)
	}

	// Add parent-child dependencies
	if err := sqliteStore.AddDependency(ctx, &types.Dependency{
		IssueID:     regularChild.ID,
		DependsOnID: epic.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.AddDependency(ctx, &types.Dependency{
		IssueID:     wispChild.ID,
		DependsOnID: epic.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatal(err)
	}

	// Epic should NOT be eligible — wisp child is still open
	epics, err := sqliteStore.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
	}

	var epicStatus *types.EpicStatus
	for _, e := range epics {
		if e.Epic.ID == "test-epic-wisp" {
			epicStatus = e
			break
		}
	}

	if epicStatus == nil {
		t.Fatal("Epic test-epic-wisp not found in results")
	}
	if epicStatus.TotalChildren != 2 {
		t.Errorf("Expected 2 total children (1 regular + 1 wisp), got %d", epicStatus.TotalChildren)
	}
	if epicStatus.ClosedChildren != 1 {
		t.Errorf("Expected 1 closed child, got %d", epicStatus.ClosedChildren)
	}
	if epicStatus.EligibleForClose {
		t.Error("Epic should NOT be eligible for close with open wisp child")
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
