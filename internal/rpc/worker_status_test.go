package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestGetWorkerStatus_NoWorkers(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// With no in_progress issues assigned, should return empty list
	result, err := client.GetWorkerStatus(&GetWorkerStatusArgs{})
	if err != nil {
		t.Fatalf("GetWorkerStatus failed: %v", err)
	}

	if len(result.Workers) != 0 {
		t.Errorf("expected 0 workers, got %d", len(result.Workers))
	}
}

func TestGetWorkerStatus_SingleWorker(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	// Create an in_progress issue with an assignee
	issue := &types.Issue{
		ID:        "bd-test1",
		Title:     "Test task",
		Status:    types.StatusInProgress,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := server.storage.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Query worker status
	result, err := client.GetWorkerStatus(&GetWorkerStatusArgs{})
	if err != nil {
		t.Fatalf("GetWorkerStatus failed: %v", err)
	}

	if len(result.Workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(result.Workers))
	}

	worker := result.Workers[0]
	if worker.Assignee != "worker1" {
		t.Errorf("expected assignee 'worker1', got '%s'", worker.Assignee)
	}
	if worker.Status != "in_progress" {
		t.Errorf("expected status 'in_progress', got '%s'", worker.Status)
	}
	if worker.LastActivity == "" {
		t.Error("expected last activity to be set")
	}
	// Not part of a molecule, so these should be empty
	if worker.MoleculeID != "" {
		t.Errorf("expected empty molecule ID, got '%s'", worker.MoleculeID)
	}
}

func TestGetWorkerStatus_WithMolecule(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	// Create a molecule (epic)
	molecule := &types.Issue{
		ID:        "bd-mol1",
		Title:     "Test Molecule",
		Status:    types.StatusOpen,
		IssueType: types.TypeEpic,
		Priority:  2,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := server.storage.CreateIssue(ctx, molecule, "test"); err != nil {
		t.Fatalf("failed to create molecule: %v", err)
	}

	// Create step 1 (completed)
	step1 := &types.Issue{
		ID:        "bd-step1",
		Title:     "Step 1: Setup",
		Status:    types.StatusClosed,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ClosedAt:  func() *time.Time { t := time.Now(); return &t }(),
	}

	if err := server.storage.CreateIssue(ctx, step1, "test"); err != nil {
		t.Fatalf("failed to create step1: %v", err)
	}

	// Create step 2 (current step - in progress)
	step2 := &types.Issue{
		ID:        "bd-step2",
		Title:     "Step 2: Implementation",
		Status:    types.StatusInProgress,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := server.storage.CreateIssue(ctx, step2, "test"); err != nil {
		t.Fatalf("failed to create step2: %v", err)
	}

	// Create step 3 (pending)
	step3 := &types.Issue{
		ID:        "bd-step3",
		Title:     "Step 3: Testing",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := server.storage.CreateIssue(ctx, step3, "test"); err != nil {
		t.Fatalf("failed to create step3: %v", err)
	}

	// Add parent-child dependencies (steps depend on molecule)
	for _, stepID := range []string{"bd-step1", "bd-step2", "bd-step3"} {
		dep := &types.Dependency{
			IssueID:     stepID,
			DependsOnID: "bd-mol1",
			Type:        types.DepParentChild,
			CreatedAt:   time.Now(),
			CreatedBy:   "test",
		}
		if err := server.storage.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency for %s: %v", stepID, err)
		}
	}

	// Query worker status
	result, err := client.GetWorkerStatus(&GetWorkerStatusArgs{})
	if err != nil {
		t.Fatalf("GetWorkerStatus failed: %v", err)
	}

	if len(result.Workers) != 1 {
		t.Fatalf("expected 1 worker (only in_progress issues), got %d", len(result.Workers))
	}

	worker := result.Workers[0]
	if worker.Assignee != "worker1" {
		t.Errorf("expected assignee 'worker1', got '%s'", worker.Assignee)
	}
	if worker.MoleculeID != "bd-mol1" {
		t.Errorf("expected molecule ID 'bd-mol1', got '%s'", worker.MoleculeID)
	}
	if worker.MoleculeTitle != "Test Molecule" {
		t.Errorf("expected molecule title 'Test Molecule', got '%s'", worker.MoleculeTitle)
	}
	if worker.StepID != "bd-step2" {
		t.Errorf("expected step ID 'bd-step2', got '%s'", worker.StepID)
	}
	if worker.StepTitle != "Step 2: Implementation" {
		t.Errorf("expected step title 'Step 2: Implementation', got '%s'", worker.StepTitle)
	}
	if worker.TotalSteps != 3 {
		t.Errorf("expected 3 total steps, got %d", worker.TotalSteps)
	}
	// Note: CurrentStep ordering depends on how GetDependents orders results
	// Just verify it's set
	if worker.CurrentStep < 1 || worker.CurrentStep > 3 {
		t.Errorf("expected current step between 1 and 3, got %d", worker.CurrentStep)
	}
}

func TestGetWorkerStatus_FilterByAssignee(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues for two different workers
	issue1 := &types.Issue{
		ID:        "bd-test1",
		Title:     "Task for worker1",
		Status:    types.StatusInProgress,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	issue2 := &types.Issue{
		ID:        "bd-test2",
		Title:     "Task for worker2",
		Status:    types.StatusInProgress,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker2",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := server.storage.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("failed to create issue1: %v", err)
	}
	if err := server.storage.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("failed to create issue2: %v", err)
	}

	// Query all workers
	allResult, err := client.GetWorkerStatus(&GetWorkerStatusArgs{})
	if err != nil {
		t.Fatalf("GetWorkerStatus (all) failed: %v", err)
	}

	if len(allResult.Workers) != 2 {
		t.Errorf("expected 2 workers, got %d", len(allResult.Workers))
	}

	// Query specific worker
	filteredResult, err := client.GetWorkerStatus(&GetWorkerStatusArgs{Assignee: "worker1"})
	if err != nil {
		t.Fatalf("GetWorkerStatus (filtered) failed: %v", err)
	}

	if len(filteredResult.Workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(filteredResult.Workers))
	}

	if filteredResult.Workers[0].Assignee != "worker1" {
		t.Errorf("expected assignee 'worker1', got '%s'", filteredResult.Workers[0].Assignee)
	}
}

func TestGetWorkerStatus_OnlyInProgressIssues(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different statuses
	openIssue := &types.Issue{
		ID:        "bd-open",
		Title:     "Open task",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	inProgressIssue := &types.Issue{
		ID:        "bd-inprog",
		Title:     "In progress task",
		Status:    types.StatusInProgress,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker2",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	closedIssue := &types.Issue{
		ID:        "bd-closed",
		Title:     "Closed task",
		Status:    types.StatusClosed,
		IssueType: types.TypeTask,
		Priority:  2,
		Assignee:  "worker3",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ClosedAt:  func() *time.Time { t := time.Now(); return &t }(),
	}

	for _, issue := range []*types.Issue{openIssue, inProgressIssue, closedIssue} {
		if err := server.storage.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Query worker status - should only return in_progress issues
	result, err := client.GetWorkerStatus(&GetWorkerStatusArgs{})
	if err != nil {
		t.Fatalf("GetWorkerStatus failed: %v", err)
	}

	if len(result.Workers) != 1 {
		t.Fatalf("expected 1 worker (only in_progress), got %d", len(result.Workers))
	}

	if result.Workers[0].Assignee != "worker2" {
		t.Errorf("expected assignee 'worker2', got '%s'", result.Workers[0].Assignee)
	}
}
