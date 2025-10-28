package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestDepAdd(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	sqliteStore := newTestStore(t, dbPath)

	ctx := context.Background()
	
	// Create test issues
	issues := []*types.Issue{
		{
			ID:        "test-1",
			Title:     "Task 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		},
		{
			ID:        "test-2",
			Title:     "Task 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		},
	}

	for _, issue := range issues {
		if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Add dependency
	dep := &types.Dependency{
		IssueID:     "test-1",
		DependsOnID: "test-2",
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
	}

	if err := sqliteStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// Verify dependency was added
	deps, err := sqliteStore.GetDependencies(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps))
	}

	if deps[0].ID != "test-2" {
		t.Errorf("Expected dependency on test-2, got %s", deps[0].ID)
	}
}

func TestDepTypes(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	sqliteStore := newTestStore(t, dbPath)

	ctx := context.Background()
	
	// Create test issues
	for i := 1; i <= 4; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("test-%d", i),
			Title:     fmt.Sprintf("Task %d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Test different dependency types (without creating cycles)
	depTypes := []struct {
		depType types.DependencyType
		from    string
		to      string
	}{
		{types.DepBlocks, "test-2", "test-1"},
		{types.DepRelated, "test-3", "test-1"},
		{types.DepParentChild, "test-4", "test-1"},
		{types.DepDiscoveredFrom, "test-3", "test-2"},
	}

	for _, dt := range depTypes {
		dep := &types.Dependency{
			IssueID:     dt.from,
			DependsOnID: dt.to,
			Type:        dt.depType,
			CreatedAt:   time.Now(),
		}

		if err := sqliteStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency failed for type %s: %v", dt.depType, err)
		}
	}
}

func TestDepCycleDetection(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	sqliteStore := newTestStore(t, dbPath)

	ctx := context.Background()
	
	// Create test issues
	for i := 1; i <= 3; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("test-%d", i),
			Title:     fmt.Sprintf("Task %d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Create a cycle: test-1 -> test-2 -> test-3 -> test-1
	// Add first two deps successfully
	deps := []struct {
		from string
		to   string
	}{
		{"test-1", "test-2"},
		{"test-2", "test-3"},
	}

	for _, d := range deps {
		dep := &types.Dependency{
			IssueID:     d.from,
			DependsOnID: d.to,
			Type:        types.DepBlocks,
			CreatedAt:   time.Now(),
		}
		if err := sqliteStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency failed: %v", err)
		}
	}
	
	// Try to add the third dep which would create a cycle - should fail
	cycleDep := &types.Dependency{
		IssueID:     "test-3",
		DependsOnID: "test-1",
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
	}
	if err := sqliteStore.AddDependency(ctx, cycleDep, "test"); err == nil {
		t.Fatal("Expected AddDependency to fail when creating cycle, but it succeeded")
	}

	// Since cycle detection prevented the cycle, DetectCycles should find no cycles
	cycles, err := sqliteStore.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Error("Expected no cycles since cycle was prevented")
	}
}

func TestDepCommandsInit(t *testing.T) {
	if depCmd == nil {
		t.Fatal("depCmd should be initialized")
	}
	
	if depCmd.Use != "dep" {
		t.Errorf("Expected Use='dep', got %q", depCmd.Use)
	}

	if depAddCmd == nil {
		t.Fatal("depAddCmd should be initialized")
	}

	if depRemoveCmd == nil {
		t.Fatal("depRemoveCmd should be initialized")
	}
}

func TestDepRemove(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	sqliteStore := newTestStore(t, dbPath)

	ctx := context.Background()
	
	// Create test issues
	issues := []*types.Issue{
		{
			ID:        "test-1",
			Title:     "Task 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		},
		{
			ID:        "test-2",
			Title:     "Task 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		},
	}

	for _, issue := range issues {
		if err := sqliteStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Add dependency
	dep := &types.Dependency{
		IssueID:     "test-1",
		DependsOnID: "test-2",
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
	}

	if err := sqliteStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatal(err)
	}

	// Remove dependency
	if err := sqliteStore.RemoveDependency(ctx, "test-1", "test-2", "test"); err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	// Verify dependency was removed
	deps, err := sqliteStore.GetDependencies(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}

	if len(deps) != 0 {
		t.Errorf("Expected 0 dependencies after removal, got %d", len(deps))
	}
}
