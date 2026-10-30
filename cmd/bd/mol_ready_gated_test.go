//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// setupGatedTestDB creates a test store on the suite's Dolt test server
// (skipped when it is not running), with the orchestrator custom types
// newTestStore configures. Closed by t.Cleanup; the returned cleanup is a
// no-op kept for the callers' defer.
func setupGatedTestDB(t *testing.T) (*dolt.DoltStore, func()) {
	t.Helper()
	store := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	return store, func() {}
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

// TestFindGateReadyMolecules_ExitGateDownstream pours the GH#3316 repro
// (prep → type = "gate" step with a human gate → execute) and walks it:
// the gate step's task becomes ready once prep closes, execute stays
// blocked after that task closes until the companion gate is resolved, and
// gate-resume discovery then reports execute as the ready step.
func TestFindGateReadyMolecules_ExitGateDownstream(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupGatedTestDB(t)
	defer cleanup()

	sg := cookGateFormulaTOML(t, "gate-repro", gateTypedReproTOML)
	result, err := spawnMolecule(ctx, store, sg, nil, "", "test", false, "")
	if err != nil {
		t.Fatalf("spawnMolecule: %v", err)
	}
	id := func(templateID string) string {
		t.Helper()
		newID, ok := result.IDMapping[templateID]
		if !ok {
			t.Fatalf("id mapping has no %s: %v", templateID, result.IDMapping)
		}
		return newID
	}
	prepID, taskID := id("gate-repro.prep"), id("gate-repro.agree")
	gateID, executeID := id("gate-repro.gate-agree"), id("gate-repro.execute")

	ready := func() map[string]bool {
		t.Helper()
		issues, err := store.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := make(map[string]bool, len(issues))
		for _, i := range issues {
			ids[i.ID] = true
		}
		return ids
	}
	closeIssue := func(issueID string) {
		t.Helper()
		if err := store.CloseIssue(ctx, issueID, "done", "test", ""); err != nil {
			t.Fatalf("CloseIssue(%s): %v", issueID, err)
		}
	}

	closeIssue(prepID)
	if r := ready(); !r[taskID] {
		t.Fatalf("gate step task %s not ready after prep closed; ready: %v", taskID, r)
	}

	closeIssue(taskID)
	if r := ready(); r[executeID] {
		t.Fatalf("execute %s is ready after the prep task closed but before the gate %s was resolved", executeID, gateID)
	}
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules: %v", err)
	}
	if len(molecules) != 0 {
		t.Fatalf("gate-ready molecules before the gate was resolved = %d, want 0", len(molecules))
	}

	// bd gate resolve closes the gate issue.
	closeIssue(gateID)
	molecules, err = findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules: %v", err)
	}
	if len(molecules) != 1 {
		t.Fatalf("gate-ready molecules = %d, want 1", len(molecules))
	}
	if molecules[0].MoleculeID != result.NewEpicID {
		t.Errorf("MoleculeID = %s, want %s", molecules[0].MoleculeID, result.NewEpicID)
	}
	if molecules[0].ReadyStep == nil || molecules[0].ReadyStep.ID != executeID {
		t.Errorf("ReadyStep = %+v, want %s", molecules[0].ReadyStep, executeID)
	}
	if molecules[0].ClosedGate == nil || molecules[0].ClosedGate.ID != gateID {
		t.Errorf("ClosedGate = %+v, want %s", molecules[0].ClosedGate, gateID)
	}
}

// pouredGateMolecule is a formula cooked and poured into a Dolt test store,
// for walking its gate wiring through the store's ready computation.
type pouredGateMolecule struct {
	t      *testing.T
	ctx    context.Context
	store  *dolt.DoltStore
	result *InstantiateResult
}

func pourGateMolecule(t *testing.T, ctx context.Context, store *dolt.DoltStore, name, src string) *pouredGateMolecule {
	t.Helper()
	sg := cookGateFormulaTOML(t, name, src)
	result, err := spawnMolecule(ctx, store, sg, nil, "", "test", false, "")
	if err != nil {
		t.Fatalf("spawnMolecule: %v", err)
	}
	return &pouredGateMolecule{t: t, ctx: ctx, store: store, result: result}
}

// id returns the poured ID of a template issue ID.
func (m *pouredGateMolecule) id(templateID string) string {
	m.t.Helper()
	newID, ok := m.result.IDMapping[templateID]
	if !ok {
		m.t.Fatalf("id mapping has no %s: %v", templateID, m.result.IDMapping)
	}
	return newID
}

func (m *pouredGateMolecule) ready() map[string]bool {
	m.t.Helper()
	issues, err := m.store.GetReadyWork(m.ctx, types.WorkFilter{})
	if err != nil {
		m.t.Fatalf("GetReadyWork: %v", err)
	}
	ids := make(map[string]bool, len(issues))
	for _, i := range issues {
		ids[i.ID] = true
	}
	return ids
}

// closeIssue closes an issue with the raw close bd gate check and bd gate
// resolve use, which ignores the issue's open blockers.
func (m *pouredGateMolecule) closeIssue(issueID string) {
	m.t.Helper()
	if err := m.store.CloseIssue(m.ctx, issueID, "done", "test", ""); err != nil {
		m.t.Fatalf("CloseIssue(%s): %v", issueID, err)
	}
}

// TestFindGateReadyMolecules_ExitGateClosedBeforeTask closes the companion
// gate of the GH#3316 repro while the gate step's task is still open, as bd
// gate check (an expired timer, a matched gh:run) and bd gate resolve can.
// The step after the gate also waits for the task, so it stays blocked and
// gate-resume discovery reports it only once the task closes.
func TestFindGateReadyMolecules_ExitGateClosedBeforeTask(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupGatedTestDB(t)
	defer cleanup()

	mol := pourGateMolecule(t, ctx, store, "gate-repro", gateTypedReproTOML)
	prepID, taskID := mol.id("gate-repro.prep"), mol.id("gate-repro.agree")
	gateID, executeID := mol.id("gate-repro.gate-agree"), mol.id("gate-repro.execute")

	mol.closeIssue(prepID)
	mol.closeIssue(gateID)
	if r := mol.ready(); r[executeID] || !r[taskID] {
		t.Fatalf("after the gate %s closed with its task open: execute ready = %v, task ready = %v; want false, true", gateID, r[executeID], r[taskID])
	}
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules: %v", err)
	}
	if len(molecules) != 0 {
		t.Fatalf("gate-ready molecules while the gate step's task %s is open = %d, want 0", taskID, len(molecules))
	}

	mol.closeIssue(taskID)
	if r := mol.ready(); !r[executeID] {
		t.Fatalf("execute %s not ready after both the task and the gate closed; ready: %v", executeID, r)
	}
	molecules, err = findGateReadyMolecules(ctx, store)
	if err != nil {
		t.Fatalf("findGateReadyMolecules: %v", err)
	}
	if len(molecules) != 1 {
		t.Fatalf("gate-ready molecules = %d, want 1", len(molecules))
	}
	if molecules[0].ReadyStep == nil || molecules[0].ReadyStep.ID != executeID {
		t.Errorf("ReadyStep = %+v, want %s", molecules[0].ReadyStep, executeID)
	}
	if molecules[0].ClosedGate == nil || molecules[0].ClosedGate.ID != gateID {
		t.Errorf("ClosedGate = %+v, want %s", molecules[0].ClosedGate, gateID)
	}
}
