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

// setupGatedTestDB creates a temporary file-based test database
func setupGatedTestDB(t *testing.T) (*sqlite.SQLiteStorage, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "bd-test-gated-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	testDB := filepath.Join(tmpDir, "test.db")
	store, err := sqlite.New(context.Background(), testDB)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create test database: %v", err)
	}

	// Set issue_prefix (required for beads)
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Configure Gas Town custom types for test compatibility (bd-find4)
	if err := store.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

// =============================================================================
// mol ready --gated Tests (bd-lhalq: Gate-resume discovery)
// =============================================================================

// TestFindGateReadyMolecules_NoGates tests finding gate-ready molecules when no gates exist
func TestFindGateReadyMolecules_NoGates(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupGatedTestDB(t)
	defer cleanup()

	// Create a regular molecule (no gates)
	mol := &types.Issue{
		ID:        "test-mol-001",
		Title:     "Test Molecule",
		IssueType: types.TypeEpic,
		Status:    types.StatusInProgress,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	step := &types.Issue{
		ID:        "test-mol-001.step1",
		Title:     "Step 1",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, mol, "test"); err != nil {
		t.Fatalf("Failed to create molecule: %v", err)
	}
	if err := store.CreateIssue(ctx, step, "test"); err != nil {
		t.Fatalf("Failed to create step: %v", err)
	}

	// Add parent-child relationship
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     step.ID,
		DependsOnID: mol.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Find gate-ready molecules
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules failed: %v", err)
	}

	if len(molecules) != 0 {
		t.Errorf("Expected 0 gate-ready molecules, got %d", len(molecules))
	}
}

// TestFindGateReadyMolecules_ClosedGate tests finding molecules with closed gates
func TestFindGateReadyMolecules_ClosedGate(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupGatedTestDB(t)
	defer cleanup()

	// Create molecule structure:
	// mol-001
	//   └── gate-await-ci (closed)
	//   └── step1 (blocked by gate-await-ci, should become ready)

	mol := &types.Issue{
		ID:        "test-mol-002",
		Title:     "Test Molecule with Gate",
		IssueType: types.TypeEpic,
		Status:    types.StatusInProgress,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	gate := &types.Issue{
		ID:        "test-mol-002.gate-await-ci",
		Title:     "Gate: gh:run ci-workflow",
		IssueType: "gate",
		Status:    types.StatusClosed, // Gate has closed
		AwaitType: "gh:run",
		AwaitID:   "ci-workflow",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	step := &types.Issue{
		ID:        "test-mol-002.step1",
		Title:     "Deploy after CI",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, mol, "test"); err != nil {
		t.Fatalf("Failed to create molecule: %v", err)
	}
	if err := store.CreateIssue(ctx, gate, "test"); err != nil {
		t.Fatalf("Failed to create gate: %v", err)
	}
	if err := store.CreateIssue(ctx, step, "test"); err != nil {
		t.Fatalf("Failed to create step: %v", err)
	}

	// Add parent-child relationships
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     gate.ID,
		DependsOnID: mol.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add gate parent-child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     step.ID,
		DependsOnID: mol.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add step parent-child: %v", err)
	}

	// Add blocking dependency: step depends on gate (gate blocks step)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     step.ID,
		DependsOnID: gate.ID,
		Type:        types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("Failed to add blocking dependency: %v", err)
	}

	// Find gate-ready molecules
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules failed: %v", err)
	}

	if len(molecules) != 1 {
		t.Errorf("Expected 1 gate-ready molecule, got %d", len(molecules))
		return
	}

	if molecules[0].MoleculeID != mol.ID {
		t.Errorf("Expected molecule ID %s, got %s", mol.ID, molecules[0].MoleculeID)
	}
	if molecules[0].ClosedGate == nil {
		t.Error("Expected closed gate to be set")
	} else if molecules[0].ClosedGate.ID != gate.ID {
		t.Errorf("Expected closed gate ID %s, got %s", gate.ID, molecules[0].ClosedGate.ID)
	}
	if molecules[0].ReadyStep == nil {
		t.Error("Expected ready step to be set")
	} else if molecules[0].ReadyStep.ID != step.ID {
		t.Errorf("Expected ready step ID %s, got %s", step.ID, molecules[0].ReadyStep.ID)
	}
}

// TestFindGateReadyMolecules_OpenGate tests that open gates don't trigger ready
func TestFindGateReadyMolecules_OpenGate(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupGatedTestDB(t)
	defer cleanup()

	// Create molecule with OPEN gate
	mol := &types.Issue{
		ID:        "test-mol-003",
		Title:     "Test Molecule with Open Gate",
		IssueType: types.TypeEpic,
		Status:    types.StatusInProgress,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	gate := &types.Issue{
		ID:        "test-mol-003.gate-await-ci",
		Title:     "Gate: gh:run ci-workflow",
		IssueType: "gate",
		Status:    types.StatusOpen, // Gate is still open
		AwaitType: "gh:run",
		AwaitID:   "ci-workflow",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	step := &types.Issue{
		ID:        "test-mol-003.step1",
		Title:     "Deploy after CI",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, mol, "test"); err != nil {
		t.Fatalf("Failed to create molecule: %v", err)
	}
	if err := store.CreateIssue(ctx, gate, "test"); err != nil {
		t.Fatalf("Failed to create gate: %v", err)
	}
	if err := store.CreateIssue(ctx, step, "test"); err != nil {
		t.Fatalf("Failed to create step: %v", err)
	}

	// Add parent-child relationships
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     gate.ID,
		DependsOnID: mol.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add gate parent-child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     step.ID,
		DependsOnID: mol.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add step parent-child: %v", err)
	}

	// Add blocking dependency: step depends on gate
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     step.ID,
		DependsOnID: gate.ID,
		Type:        types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("Failed to add blocking dependency: %v", err)
	}

	// Find gate-ready molecules
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules failed: %v", err)
	}

	if len(molecules) != 0 {
		t.Errorf("Expected 0 gate-ready molecules (gate is open), got %d", len(molecules))
	}
}

// TestFindGateReadyMolecules_HookedMolecule tests that hooked molecules are filtered out
func TestFindGateReadyMolecules_HookedMolecule(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupGatedTestDB(t)
	defer cleanup()

	// Create molecule with closed gate, but molecule is hooked
	mol := &types.Issue{
		ID:        "test-mol-004",
		Title:     "Test Hooked Molecule",
		IssueType: types.TypeEpic,
		Status:    types.StatusHooked, // Already hooked by an agent
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	gate := &types.Issue{
		ID:        "test-mol-004.gate-await-ci",
		Title:     "Gate: gh:run ci-workflow",
		IssueType: "gate",
		Status:    types.StatusClosed,
		AwaitType: "gh:run",
		AwaitID:   "ci-workflow",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	step := &types.Issue{
		ID:        "test-mol-004.step1",
		Title:     "Deploy after CI",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, mol, "test"); err != nil {
		t.Fatalf("Failed to create molecule: %v", err)
	}
	if err := store.CreateIssue(ctx, gate, "test"); err != nil {
		t.Fatalf("Failed to create gate: %v", err)
	}
	if err := store.CreateIssue(ctx, step, "test"); err != nil {
		t.Fatalf("Failed to create step: %v", err)
	}

	// Add parent-child relationships
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     gate.ID,
		DependsOnID: mol.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add gate parent-child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     step.ID,
		DependsOnID: mol.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add step parent-child: %v", err)
	}

	// Add blocking dependency
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     step.ID,
		DependsOnID: gate.ID,
		Type:        types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("Failed to add blocking dependency: %v", err)
	}

	// Find gate-ready molecules
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules failed: %v", err)
	}

	if len(molecules) != 0 {
		t.Errorf("Expected 0 gate-ready molecules (molecule is hooked), got %d", len(molecules))
	}
}

// TestFindGateReadyMolecules_MultipleGates tests handling multiple closed gates
func TestFindGateReadyMolecules_MultipleGates(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupGatedTestDB(t)
	defer cleanup()

	// Create two molecules, each with a closed gate
	for i := 1; i <= 2; i++ {
		molID := fmt.Sprintf("test-multi-%d", i)
		mol := &types.Issue{
			ID:        molID,
			Title:     fmt.Sprintf("Multi Gate Mol %d", i),
			IssueType: types.TypeEpic,
			Status:    types.StatusInProgress,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		gate := &types.Issue{
			ID:        fmt.Sprintf("%s.gate", molID),
			Title:     "Gate: gh:run",
			IssueType: "gate",
			Status:    types.StatusClosed,
			AwaitType: "gh:run",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		step := &types.Issue{
			ID:        fmt.Sprintf("%s.step1", molID),
			Title:     "Step 1",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		if err := store.CreateIssue(ctx, mol, "test"); err != nil {
			t.Fatalf("Failed to create molecule %d: %v", i, err)
		}
		if err := store.CreateIssue(ctx, gate, "test"); err != nil {
			t.Fatalf("Failed to create gate %d: %v", i, err)
		}
		if err := store.CreateIssue(ctx, step, "test"); err != nil {
			t.Fatalf("Failed to create step %d: %v", i, err)
		}

		// Add dependencies
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID:     gate.ID,
			DependsOnID: mol.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add gate parent-child %d: %v", i, err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID:     step.ID,
			DependsOnID: mol.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add step parent-child %d: %v", i, err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID:     step.ID,
			DependsOnID: gate.ID,
			Type:        types.DepBlocks,
		}, "test"); err != nil {
			t.Fatalf("Failed to add blocking dep %d: %v", i, err)
		}
	}

	// Find gate-ready molecules
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules failed: %v", err)
	}

	if len(molecules) != 2 {
		t.Errorf("Expected 2 gate-ready molecules, got %d", len(molecules))
	}
}
