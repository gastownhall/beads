package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Helper function to test adding a dependency with a specific type
func testAddDependencyWithType(t *testing.T, depType types.DependencyType, title1, title2 string) {
	t.Helper()

	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two issues
	issue1 := &types.Issue{Title: title1, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: title2, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	// Add dependency (issue2 depends on issue1)
	dep := &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        depType,
	}

	err := store.AddDependency(ctx, dep, "test-user")
	if err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// Verify dependency was added
	deps, err := store.GetDependencies(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps))
	}

	if deps[0].ID != issue1.ID {
		t.Errorf("Expected dependency on %s, got %s", issue1.ID, deps[0].ID)
	}
}

func TestAddDependency(t *testing.T) {
	testAddDependencyWithType(t, types.DepBlocks, "First", "Second")
}

func TestAddDependencyDiscoveredFrom(t *testing.T) {
	testAddDependencyWithType(t, types.DepDiscoveredFrom, "Parent task", "Bug found during work")
}

func TestParentChildValidation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an epic (parent) and a task (child)
	epic := &types.Issue{Title: "Epic Feature", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	task := &types.Issue{Title: "Subtask", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, epic, "test-user")
	store.CreateIssue(ctx, task, "test-user")

	// Test 1: Valid direction - Task depends on Epic (child belongs to parent)
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     task.ID,
		DependsOnID: epic.ID,
		Type:        types.DepParentChild,
	}, "test-user")
	if err != nil {
		t.Fatalf("Valid parent-child dependency failed: %v", err)
	}

	// Verify it was added
	deps, err := store.GetDependencies(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps))
	}

	// Remove the dependency for next test
	err = store.RemoveDependency(ctx, task.ID, epic.ID, "test-user")
	if err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	// Test 2: Invalid direction - Epic depends on Task (parent depends on child - backwards!)
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     epic.ID,
		DependsOnID: task.ID,
		Type:        types.DepParentChild,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected error when parent depends on child, but got none")
	}
	if !strings.Contains(err.Error(), "child") || !strings.Contains(err.Error(), "parent") {
		t.Errorf("Expected error message to mention child/parent relationship, got: %v", err)
	}
}

func TestRemoveDependency(t *testing.T) {
	env := newTestEnv(t)

	issue1 := env.CreateIssue("First")
	issue2 := env.CreateIssue("Second")
	env.AddDep(issue2, issue1)

	// Remove the dependency
	err := env.Store.RemoveDependency(env.Ctx, issue2.ID, issue1.ID, "test-user")
	if err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	// Verify dependency was removed
	deps, err := env.Store.GetDependencies(env.Ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("Expected 0 dependencies after removal, got %d", len(deps))
	}
}

func TestAddDependencyPreservesProvidedMetadata(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	parent := &types.Issue{Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	child := &types.Issue{Title: "Child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	store.CreateIssue(ctx, parent, "test-user")
	store.CreateIssue(ctx, child, "test-user")

	customTime := time.Date(2024, 10, 24, 12, 0, 0, 0, time.UTC)

	dep := &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent.ID,
		Type:        types.DepParentChild,
		CreatedAt:   customTime,
		CreatedBy:   "import",
	}

	if err := store.AddDependency(ctx, dep, "test-user"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	records, err := store.GetDependencyRecords(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Expected 1 dependency record, got %d", len(records))
	}
	got := records[0]
	if !got.CreatedAt.Equal(customTime) {
		t.Fatalf("Expected CreatedAt %v, got %v", customTime, got.CreatedAt)
	}
	if got.CreatedBy != "import" {
		t.Fatalf("Expected CreatedBy 'import', got %q", got.CreatedBy)
	}
}

func TestGetDependents(t *testing.T) {
	env := newTestEnv(t)

	// Create issues: issue2 and issue3 both depend on issue1
	issue1 := env.CreateIssue("Foundation")
	issue2 := env.CreateIssue("Feature A")
	issue3 := env.CreateIssue("Feature B")

	env.AddDep(issue2, issue1)
	env.AddDep(issue3, issue1)

	// Get dependents of issue1
	dependents, err := env.Store.GetDependents(env.Ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependents failed: %v", err)
	}
	if len(dependents) != 2 {
		t.Fatalf("Expected 2 dependents, got %d", len(dependents))
	}

	// Verify both dependents are present
	foundIDs := make(map[string]bool)
	for _, dep := range dependents {
		foundIDs[dep.ID] = true
	}
	if !foundIDs[issue2.ID] || !foundIDs[issue3.ID] {
		t.Errorf("Expected dependents %s and %s", issue2.ID, issue3.ID)
	}
}

func TestGetDependencyTree(t *testing.T) {
	env := newTestEnv(t)

	// Create a chain: issue3 → issue2 → issue1
	issue1 := env.CreateIssue("Level 0")
	issue2 := env.CreateIssue("Level 1")
	issue3 := env.CreateIssue("Level 2")

	env.AddDep(issue2, issue1)
	env.AddDep(issue3, issue2)

	// Get tree starting from issue3
	tree, err := env.Store.GetDependencyTree(env.Ctx, issue3.ID, 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}
	if len(tree) != 3 {
		t.Fatalf("Expected 3 nodes in tree, got %d", len(tree))
	}

	// Verify depths
	depthMap := make(map[string]int)
	for _, node := range tree {
		depthMap[node.ID] = node.Depth
	}
	if depthMap[issue3.ID] != 0 {
		t.Errorf("Expected depth 0 for %s, got %d", issue3.ID, depthMap[issue3.ID])
	}
	if depthMap[issue2.ID] != 1 {
		t.Errorf("Expected depth 1 for %s, got %d", issue2.ID, depthMap[issue2.ID])
	}
	if depthMap[issue1.ID] != 2 {
		t.Errorf("Expected depth 2 for %s, got %d", issue1.ID, depthMap[issue1.ID])
	}
}

func TestGetDependencyTree_TruncationDepth(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a long chain: bd-5 → bd-4 → bd-3 → bd-2 → bd-1
	issues := make([]*types.Issue, 5)
	for i := 0; i < 5; i++ {
		issues[i] = &types.Issue{
			Title:     fmt.Sprintf("Level %d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		err := store.CreateIssue(ctx, issues[i], "test-user")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Link them in chain
	for i := 1; i < 5; i++ {
		err := store.AddDependency(ctx, &types.Dependency{
			IssueID:     issues[i].ID,
			DependsOnID: issues[i-1].ID,
			Type:        types.DepBlocks,
		}, "test-user")
		if err != nil {
			t.Fatalf("AddDependency failed: %v", err)
		}
	}

	// Get tree with maxDepth=2 (should only get 3 nodes: depths 0, 1, 2)
	tree, err := store.GetDependencyTree(ctx, issues[4].ID, 2, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}

	if len(tree) != 3 {
		t.Fatalf("Expected 3 nodes with maxDepth=2, got %d", len(tree))
	}

	// Check that last node is marked as truncated
	foundTruncated := false
	for _, node := range tree {
		if node.Depth == 2 && node.Truncated {
			foundTruncated = true
			break
		}
	}

	if !foundTruncated {
		t.Error("Expected node at depth 2 to be marked as truncated")
	}
}

func TestGetDependencyTree_DefaultDepth(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a simple chain
	issue1 := &types.Issue{Title: "Level 0", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Level 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepBlocks,
	}, "test-user")

	// Get tree with default depth (50)
	tree, err := store.GetDependencyTree(ctx, issue2.ID, 50, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}

	if len(tree) != 2 {
		t.Fatalf("Expected 2 nodes, got %d", len(tree))
	}

	// No truncation should occur
	for _, node := range tree {
		if node.Truncated {
			t.Error("Expected no truncation with default depth on short chain")
		}
	}
}

func TestGetDependencyTree_MaxDepthOne(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a chain: bd-3 → bd-2 → bd-1
	issue1 := &types.Issue{Title: "Level 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Level 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepBlocks,
	}, "test-user")

	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue3.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepBlocks,
	}, "test-user")

	// Get tree with maxDepth=1 (should get root + one level)
	tree, err := store.GetDependencyTree(ctx, issue3.ID, 1, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}

	// Should get root (depth 0) and one child (depth 1)
	if len(tree) != 2 {
		t.Fatalf("Expected 2 nodes with maxDepth=1, got %d", len(tree))
	}

	// Check root is at depth 0 and not truncated
	rootFound := false
	for _, node := range tree {
		if node.ID == issue3.ID && node.Depth == 0 && !node.Truncated {
			rootFound = true
		}
	}
	if !rootFound {
		t.Error("Expected root at depth 0, not truncated")
	}

	// Check child at depth 1 is truncated
	childTruncated := false
	for _, node := range tree {
		if node.Depth == 1 && node.Truncated {
			childTruncated = true
		}
	}
	if !childTruncated {
		t.Error("Expected child at depth 1 to be truncated")
	}
}

func TestDetectCycles(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Try to create a cycle: bd-1 → bd-2 → bd-3 → bd-1
	// This should be prevented by AddDependency
	issue1 := &types.Issue{Title: "First", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Second", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Third", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	// Add first two dependencies successfully
	err := store.AddDependency(ctx, &types.Dependency{IssueID: issue1.ID, DependsOnID: issue2.ID, Type: types.DepBlocks}, "test-user")
	if err != nil {
		t.Fatalf("First dependency failed: %v", err)
	}

	err = store.AddDependency(ctx, &types.Dependency{IssueID: issue2.ID, DependsOnID: issue3.ID, Type: types.DepBlocks}, "test-user")
	if err != nil {
		t.Fatalf("Second dependency failed: %v", err)
	}

	// The third dependency should fail because it would create a cycle
	err = store.AddDependency(ctx, &types.Dependency{IssueID: issue3.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")
	if err == nil {
		t.Fatal("Expected error when creating cycle, but got none")
	}

	// Verify no cycles exist
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles after prevention, but found %d", len(cycles))
	}
}

func TestNoCyclesDetected(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a valid chain with no cycles
	issue1 := &types.Issue{Title: "First", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Second", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	store.AddDependency(ctx, &types.Dependency{IssueID: issue2.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")

	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles, but found %d", len(cycles))
	}
}

func TestCrossTypeCyclePrevention(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues for cross-type cycle test
	issue1 := &types.Issue{Title: "Task A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Task B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	// Add: issue1 blocks issue2
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue1.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("First dependency (blocks) failed: %v", err)
	}

	// Try to add: issue2 parent-child issue1 (this would create a cross-type cycle)
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepParentChild,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected error when creating cross-type cycle, but got none")
	}

	// Verify no cycles exist
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles after prevention, but found %d", len(cycles))
	}
}

func TestCrossTypeCyclePreventionDiscoveredFrom(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues
	issue1 := &types.Issue{Title: "Parent Task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Bug Found", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	// Add: issue2 discovered-from issue1
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepDiscoveredFrom,
	}, "test-user")
	if err != nil {
		t.Fatalf("First dependency (discovered-from) failed: %v", err)
	}

	// Try to add: issue1 blocks issue2 (this would create a cross-type cycle)
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue1.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepBlocks,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected error when creating cross-type cycle with discovered-from, but got none")
	}
}

func TestSelfDependencyPrevention(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	issue := &types.Issue{Title: "Task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	store.CreateIssue(ctx, issue, "test-user")

	// Try to create self-dependency (issue depends on itself)
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: issue.ID,
		Type:        types.DepBlocks,
	}, "test-user")

	if err == nil {
		t.Fatal("Expected error when creating self-dependency, but got none")
	}

	if !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Errorf("Expected self-dependency error message, got: %v", err)
	}
}

func TestRelatedTypeCyclePrevention(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	issue1 := &types.Issue{Title: "Task A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Task B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	// Add: issue1 related issue2
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue1.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepRelated,
	}, "test-user")
	if err != nil {
		t.Fatalf("First dependency (related) failed: %v", err)
	}

	// Try to add: issue2 related issue1 (this creates a 2-node cycle with related type)
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepRelated,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected error when creating related-type cycle, but got none")
	}
}

func TestMixedTypeRelatedCyclePrevention(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	issue1 := &types.Issue{Title: "Task A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Task B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	// Add: issue1 blocks issue2
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue1.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("First dependency (blocks) failed: %v", err)
	}

	// Try to add: issue2 related issue1 (this creates a cross-type cycle)
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepRelated,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected error when creating blocks+related cycle, but got none")
	}
}

func TestCrossTypeCyclePreventionThreeIssues(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues for 3-node cross-type cycle test
	issue1 := &types.Issue{Title: "Task A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Task B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Task C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	// Add: issue1 blocks issue2
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue1.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("First dependency failed: %v", err)
	}

	// Add: issue2 parent-child issue3
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue3.ID,
		Type:        types.DepParentChild,
	}, "test-user")
	if err != nil {
		t.Fatalf("Second dependency failed: %v", err)
	}

	// Try to add: issue3 discovered-from issue1 (this would create a 3-node cross-type cycle)
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue3.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepDiscoveredFrom,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected error when creating 3-node cross-type cycle, but got none")
	}

	// Verify no cycles exist
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("Expected no cycles after prevention, but found %d", len(cycles))
	}
}

func TestGetDependencyTree_Reverse(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create a dependency chain: issue1 <- issue2 <- issue3
	// (issue3 depends on issue2, issue2 depends on issue1)
	issue1 := &types.Issue{
		Title:     "Base issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	issue2 := &types.Issue{
		Title:     "Depends on issue1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	issue3 := &types.Issue{
		Title:     "Depends on issue2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	store.CreateIssue(ctx, issue1, "test")
	store.CreateIssue(ctx, issue2, "test")
	store.CreateIssue(ctx, issue3, "test")

	// Create dependencies: issue3 → issue2 → issue1
	dep1 := &types.Dependency{IssueID: issue2.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}
	dep2 := &types.Dependency{IssueID: issue3.ID, DependsOnID: issue2.ID, Type: types.DepBlocks}
	store.AddDependency(ctx, dep1, "test")
	store.AddDependency(ctx, dep2, "test")

	// Test normal mode: from issue3, should traverse UP to issue1
	normalTree, err := store.GetDependencyTree(ctx, issue3.ID, 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree normal mode failed: %v", err)
	}
	if len(normalTree) != 3 {
		t.Fatalf("Expected 3 nodes in normal tree, got %d", len(normalTree))
	}

	// Test reverse mode: from issue1, should traverse DOWN to issue3
	reverseTree, err := store.GetDependencyTree(ctx, issue1.ID, 10, false, true)
	if err != nil {
		t.Fatalf("GetDependencyTree reverse mode failed: %v", err)
	}
	if len(reverseTree) != 3 {
		t.Fatalf("Expected 3 nodes in reverse tree, got %d", len(reverseTree))
	}

	// Verify reverse tree structure: issue1 at depth 0
	depthMap := make(map[string]int)
	for _, node := range reverseTree {
		depthMap[node.ID] = node.Depth
	}

	if depthMap[issue1.ID] != 0 {
		t.Errorf("Expected depth 0 for %s in reverse tree, got %d", issue1.ID, depthMap[issue1.ID])
	}

	// issue2 should be at depth 1 (depends on issue1)
	if depthMap[issue2.ID] != 1 {
		t.Errorf("Expected depth 1 for %s in reverse tree, got %d", issue2.ID, depthMap[issue2.ID])
	}

	// issue3 should be at depth 2 (depends on issue2)
	if depthMap[issue3.ID] != 2 {
		t.Errorf("Expected depth 2 for %s in reverse tree, got %d", issue3.ID, depthMap[issue3.ID])
	}
}

func TestGetDependencyTree_SubstringBug(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create 10 issues so we have both bd-1 and bd-10 (substring issue)
	// The bug: when traversing from bd-10, bd-1 gets incorrectly excluded
	// because "bd-10" contains "bd-1" as a substring
	issues := make([]*types.Issue, 10)
	for i := 0; i < 10; i++ {
		issues[i] = &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i+1),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		err := store.CreateIssue(ctx, issues[i], "test-user")
		if err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Create chain: bd-10 → bd-9 → bd-8 → bd-2 → bd-1
	// This tests the substring bug where bd-1 should appear but won't due to substring matching
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issues[9].ID, // bd-10
		DependsOnID: issues[8].ID, // bd-9
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("AddDependency bd-10→bd-9 failed: %v", err)
	}

	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issues[8].ID, // bd-9
		DependsOnID: issues[7].ID, // bd-8
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("AddDependency bd-9→bd-8 failed: %v", err)
	}

	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issues[7].ID, // bd-8
		DependsOnID: issues[1].ID, // bd-2
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("AddDependency bd-8→bd-2 failed: %v", err)
	}

	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issues[1].ID, // bd-2
		DependsOnID: issues[0].ID, // bd-1
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("AddDependency bd-2→bd-1 failed: %v", err)
	}

	// Get tree starting from bd-10
	tree, err := store.GetDependencyTree(ctx, issues[9].ID, 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}

	// Create map of issue IDs in tree for easy checking
	treeIDs := make(map[string]bool)
	for _, node := range tree {
		treeIDs[node.ID] = true
	}

	// Verify all issues in the chain appear in the tree
	// This is the KEY test: bd-1 should be in the tree
	// With the substring bug, bd-1 will be missing because "bd-10" contains "bd-1"
	expectedIssues := []int{9, 8, 7, 1, 0} // bd-10, bd-9, bd-8, bd-2, bd-1
	for _, idx := range expectedIssues {
		if !treeIDs[issues[idx].ID] {
			t.Errorf("Expected %s in dependency tree, but it was missing (substring bug)", issues[idx].ID)
		}
	}

	// Verify we have the correct number of nodes
	if len(tree) != 5 {
		t.Errorf("Expected 5 nodes in tree, got %d. Missing nodes indicate substring bug.", len(tree))
	}

	// Verify depths are correct
	depthMap := make(map[string]int)
	for _, node := range tree {
		depthMap[node.ID] = node.Depth
	}

	// Check depths: bd-10(0) → bd-9(1) → bd-8(2) → bd-2(3) → bd-1(4)
	if depthMap[issues[9].ID] != 0 {
		t.Errorf("Expected bd-10 at depth 0, got %d", depthMap[issues[9].ID])
	}
	if depthMap[issues[0].ID] != 4 {
		t.Errorf("Expected bd-1 at depth 4, got %d", depthMap[issues[0].ID])
	}
}

func TestGetDependencyCounts(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a network of issues with dependencies
	//   A (depends on B, C)
	//   B (depends on C)
	//   C (no dependencies)
	//   D (depends on A)
	//   E (no dependencies, no dependents)
	issueA := &types.Issue{Title: "Task A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{Title: "Task B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{Title: "Task C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueD := &types.Issue{Title: "Task D", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueE := &types.Issue{Title: "Task E", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueA, "test-user")
	store.CreateIssue(ctx, issueB, "test-user")
	store.CreateIssue(ctx, issueC, "test-user")
	store.CreateIssue(ctx, issueD, "test-user")
	store.CreateIssue(ctx, issueE, "test-user")

	// Add dependencies
	store.AddDependency(ctx, &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueA.ID, DependsOnID: issueC.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueB.ID, DependsOnID: issueC.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueD.ID, DependsOnID: issueA.ID, Type: types.DepBlocks}, "test-user")

	// Get counts for all issues
	issueIDs := []string{issueA.ID, issueB.ID, issueC.ID, issueD.ID, issueE.ID}
	counts, err := store.GetDependencyCounts(ctx, issueIDs)
	if err != nil {
		t.Fatalf("GetDependencyCounts failed: %v", err)
	}

	// Verify counts
	testCases := []struct {
		issueID         string
		name            string
		expectedDeps    int
		expectedDepents int
	}{
		{issueA.ID, "A", 2, 1}, // depends on B and C, D depends on A
		{issueB.ID, "B", 1, 1}, // depends on C, A depends on B
		{issueC.ID, "C", 0, 2}, // no dependencies, A and B depend on C
		{issueD.ID, "D", 1, 0}, // depends on A, nothing depends on D
		{issueE.ID, "E", 0, 0}, // isolated issue
	}

	for _, tc := range testCases {
		count := counts[tc.issueID]
		if count == nil {
			t.Errorf("Issue %s (%s): no counts returned", tc.name, tc.issueID)
			continue
		}
		if count.DependencyCount != tc.expectedDeps {
			t.Errorf("Issue %s (%s): expected %d dependencies, got %d",
				tc.name, tc.issueID, tc.expectedDeps, count.DependencyCount)
		}
		if count.DependentCount != tc.expectedDepents {
			t.Errorf("Issue %s (%s): expected %d dependents, got %d",
				tc.name, tc.issueID, tc.expectedDepents, count.DependentCount)
		}
	}
}

func TestGetDependencyCountsEmpty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Test with empty list
	counts, err := store.GetDependencyCounts(ctx, []string{})
	if err != nil {
		t.Fatalf("GetDependencyCounts failed on empty list: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("Expected empty map for empty input, got %d entries", len(counts))
	}
}

func TestGetDependencyCountsNonexistent(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Test with non-existent issue IDs
	counts, err := store.GetDependencyCounts(ctx, []string{"fake-1", "fake-2"})
	if err != nil {
		t.Fatalf("GetDependencyCounts failed on nonexistent IDs: %v", err)
	}

	// Should return zero counts for non-existent issues
	for id, count := range counts {
		if count.DependencyCount != 0 || count.DependentCount != 0 {
			t.Errorf("Expected zero counts for nonexistent issue %s, got deps=%d, dependents=%d",
				id, count.DependencyCount, count.DependentCount)
		}
	}
}

func TestGetDependenciesWithMetadata(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues
	issue1 := &types.Issue{Title: "Foundation", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Feature A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Feature B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	// Add dependencies with different types
	// issue2 depends on issue1 (blocks)
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepBlocks,
	}, "test-user")

	// issue3 depends on issue1 (discovered-from)
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue3.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepDiscoveredFrom,
	}, "test-user")

	// Get dependencies with metadata for issue2
	deps, err := store.GetDependenciesWithMetadata(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps))
	}

	// Verify the dependency includes type metadata
	dep := deps[0]
	if dep.ID != issue1.ID {
		t.Errorf("Expected dependency on %s, got %s", issue1.ID, dep.ID)
	}
	if dep.DependencyType != types.DepBlocks {
		t.Errorf("Expected dependency type 'blocks', got %s", dep.DependencyType)
	}
	if dep.Title != "Foundation" {
		t.Errorf("Expected title 'Foundation', got %s", dep.Title)
	}

	// Get dependencies with metadata for issue3
	deps3, err := store.GetDependenciesWithMetadata(ctx, issue3.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}

	if len(deps3) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(deps3))
	}

	// Verify the dependency type is discovered-from
	if deps3[0].DependencyType != types.DepDiscoveredFrom {
		t.Errorf("Expected dependency type 'discovered-from', got %s", deps3[0].DependencyType)
	}
}

func TestGetDependentsWithMetadata(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues: issue2 and issue3 both depend on issue1
	issue1 := &types.Issue{Title: "Foundation", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Feature A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Feature B", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	// Add dependencies with different types
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepBlocks,
	}, "test-user")

	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issue3.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepRelated,
	}, "test-user")

	// Get dependents of issue1 with metadata
	dependents, err := store.GetDependentsWithMetadata(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependentsWithMetadata failed: %v", err)
	}

	if len(dependents) != 2 {
		t.Fatalf("Expected 2 dependents, got %d", len(dependents))
	}

	// Verify dependents are ordered by priority (issue2=P1 before issue3=P2)
	if dependents[0].ID != issue2.ID {
		t.Errorf("Expected first dependent to be %s, got %s", issue2.ID, dependents[0].ID)
	}
	if dependents[0].DependencyType != types.DepBlocks {
		t.Errorf("Expected first dependent type 'blocks', got %s", dependents[0].DependencyType)
	}

	if dependents[1].ID != issue3.ID {
		t.Errorf("Expected second dependent to be %s, got %s", issue3.ID, dependents[1].ID)
	}
	if dependents[1].DependencyType != types.DepRelated {
		t.Errorf("Expected second dependent type 'related', got %s", dependents[1].DependencyType)
	}
}

func TestGetDependenciesWithMetadataEmpty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issue with no dependencies
	issue := &types.Issue{Title: "Standalone", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	store.CreateIssue(ctx, issue, "test-user")

	// Get dependencies with metadata
	deps, err := store.GetDependenciesWithMetadata(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}

	if len(deps) != 0 {
		t.Errorf("Expected 0 dependencies, got %d", len(deps))
	}
}

func TestGetDependenciesWithMetadataMultipleTypes(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues
	base := &types.Issue{Title: "Base", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	blocks := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	related := &types.Issue{Title: "Related", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	discovered := &types.Issue{Title: "Discovered", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, base, "test-user")
	store.CreateIssue(ctx, blocks, "test-user")
	store.CreateIssue(ctx, related, "test-user")
	store.CreateIssue(ctx, discovered, "test-user")

	// Add dependencies of different types
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     base.ID,
		DependsOnID: blocks.ID,
		Type:        types.DepBlocks,
	}, "test-user")

	store.AddDependency(ctx, &types.Dependency{
		IssueID:     base.ID,
		DependsOnID: related.ID,
		Type:        types.DepRelated,
	}, "test-user")

	store.AddDependency(ctx, &types.Dependency{
		IssueID:     base.ID,
		DependsOnID: discovered.ID,
		Type:        types.DepDiscoveredFrom,
	}, "test-user")

	// Get all dependencies with metadata
	deps, err := store.GetDependenciesWithMetadata(ctx, base.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}

	if len(deps) != 3 {
		t.Fatalf("Expected 3 dependencies, got %d", len(deps))
	}

	// Create a map of dependency types
	typeMap := make(map[string]types.DependencyType)
	for _, dep := range deps {
		typeMap[dep.ID] = dep.DependencyType
	}

	// Verify all types are correctly returned
	if typeMap[blocks.ID] != types.DepBlocks {
		t.Errorf("Expected blocks dependency type 'blocks', got %s", typeMap[blocks.ID])
	}
	if typeMap[related.ID] != types.DepRelated {
		t.Errorf("Expected related dependency type 'related', got %s", typeMap[related.ID])
	}
	if typeMap[discovered.ID] != types.DepDiscoveredFrom {
		t.Errorf("Expected discovered dependency type 'discovered-from', got %s", typeMap[discovered.ID])
	}
}

// TestGetDependencyTree_ComplexDiamond tests a diamond dependency pattern
func TestGetDependencyTree_ComplexDiamond(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create diamond pattern:
	//     D
	//    / \
	//   B   C
	//    \ /
	//     A
	issueA := &types.Issue{Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{Title: "C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueD := &types.Issue{Title: "D", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueA, "test-user")
	store.CreateIssue(ctx, issueB, "test-user")
	store.CreateIssue(ctx, issueC, "test-user")
	store.CreateIssue(ctx, issueD, "test-user")

	// Create dependencies: D blocks B, D blocks C, B blocks A, C blocks A
	store.AddDependency(ctx, &types.Dependency{IssueID: issueB.ID, DependsOnID: issueD.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueC.ID, DependsOnID: issueD.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueA.ID, DependsOnID: issueC.ID, Type: types.DepBlocks}, "test-user")

	// Get tree from A
	tree, err := store.GetDependencyTree(ctx, issueA.ID, 50, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}

	// Should have all 4 nodes
	if len(tree) != 4 {
		t.Fatalf("Expected 4 nodes in diamond, got %d", len(tree))
	}

	// Verify all expected nodes are present
	idSet := make(map[string]bool)
	for _, node := range tree {
		idSet[node.ID] = true
	}

	expected := []string{issueA.ID, issueB.ID, issueC.ID, issueD.ID}
	for _, id := range expected {
		if !idSet[id] {
			t.Errorf("Expected node %s in diamond tree", id)
		}
	}
}

// TestGetDependencyTree_ShowAllPaths tests the showAllPaths flag behavior
func TestGetDependencyTree_ShowAllPaths(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create diamond again
	issueA := &types.Issue{Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{Title: "C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueD := &types.Issue{Title: "D", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueA, "test-user")
	store.CreateIssue(ctx, issueB, "test-user")
	store.CreateIssue(ctx, issueC, "test-user")
	store.CreateIssue(ctx, issueD, "test-user")

	store.AddDependency(ctx, &types.Dependency{IssueID: issueB.ID, DependsOnID: issueD.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueC.ID, DependsOnID: issueD.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueA.ID, DependsOnID: issueC.ID, Type: types.DepBlocks}, "test-user")

	// Get tree with showAllPaths=true
	treeAll, err := store.GetDependencyTree(ctx, issueA.ID, 50, true, false)
	if err != nil {
		t.Fatalf("GetDependencyTree with showAllPaths failed: %v", err)
	}

	// Get tree with showAllPaths=false
	treeDedup, err := store.GetDependencyTree(ctx, issueA.ID, 50, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree without showAllPaths failed: %v", err)
	}

	// Both should have at least the core nodes
	if len(treeAll) < len(treeDedup) {
		t.Errorf("showAllPaths=true should have >= nodes than showAllPaths=false: got %d vs %d", len(treeAll), len(treeDedup))
	}
}

// TestGetDependencyTree_ReverseDirection tests getting dependents instead of dependencies
func TestGetDependencyTree_ReverseDirection(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a chain: A depends on B, B depends on C
	// So: B blocks A, C blocks B
	// Normal (down): From A we get [A, B, C] (dependencies)
	// Reverse (up): From C we get [C, B, A] (dependents)
	issueA := &types.Issue{Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{Title: "C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueA, "test-user")
	store.CreateIssue(ctx, issueB, "test-user")
	store.CreateIssue(ctx, issueC, "test-user")

	// A depends on B, B depends on C
	store.AddDependency(ctx, &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issueB.ID, DependsOnID: issueC.ID, Type: types.DepBlocks}, "test-user")

	// Get normal tree from A (should get A as root, then dependencies B, C)
	downTree, err := store.GetDependencyTree(ctx, issueA.ID, 50, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree down failed: %v", err)
	}

	// Get reverse tree from C (should get C as root, then dependents B, A)
	upTree, err := store.GetDependencyTree(ctx, issueC.ID, 50, false, true)
	if err != nil {
		t.Fatalf("GetDependencyTree reverse failed: %v", err)
	}

	// Both should include their root nodes
	if len(downTree) < 1 {
		t.Fatal("Down tree should include at least the root node A")
	}
	if len(upTree) < 1 {
		t.Fatal("Up tree should include at least the root node C")
	}

	// Down tree should start with A at depth 0
	if downTree[0].ID != issueA.ID {
		t.Errorf("Down tree should start with A, got %s", downTree[0].ID)
	}

	// Up tree should start with C at depth 0
	if upTree[0].ID != issueC.ID {
		t.Errorf("Up tree should start with C, got %s", upTree[0].ID)
	}

	// Down tree from A should have B and C as dependencies
	downHasB := false
	downHasC := false
	for _, node := range downTree {
		if node.ID == issueB.ID {
			downHasB = true
		}
		if node.ID == issueC.ID {
			downHasC = true
		}
	}
	if !downHasB || !downHasC {
		t.Error("Down tree from A should include B and C as dependencies")
	}

	// Up tree from C should have B and A as dependents
	upHasB := false
	upHasA := false
	for _, node := range upTree {
		if node.ID == issueB.ID {
			upHasB = true
		}
		if node.ID == issueA.ID {
			upHasA = true
		}
	}
	if !upHasB || !upHasA {
		t.Error("Up tree from C should include B and A as dependents")
	}
}

// TestDetectCycles_SingleCyclePrevention verifies single-issue cycles are caught
func TestDetectCycles_PreventionAtAddTime(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two issues
	issueA := &types.Issue{Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueA, "test-user")
	store.CreateIssue(ctx, issueB, "test-user")

	// Add A -> B
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issueA.ID,
		DependsOnID: issueB.ID,
		Type:        types.DepBlocks,
	}, "test-user")
	if err != nil {
		t.Fatalf("First AddDependency failed: %v", err)
	}

	// Try to add B -> A (would create cycle) - should fail
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issueB.ID,
		DependsOnID: issueA.ID,
		Type:        types.DepBlocks,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected AddDependency to fail when creating 2-node cycle")
	}

	// Verify no cycles exist
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}
	if len(cycles) != 0 {
		t.Error("Expected no cycles since cycle was prevented at add time")
	}
}

// TestDetectCycles_LongerCycle tests detection of longer cycles
func TestDetectCycles_LongerCyclePrevention(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a chain: A -> B -> C
	issues := make(map[string]*types.Issue)
	for _, name := range []string{"A", "B", "C"} {
		issue := &types.Issue{Title: name, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		store.CreateIssue(ctx, issue, "test-user")
		issues[name] = issue
	}

	// Build chain A -> B -> C
	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issues["A"].ID,
		DependsOnID: issues["B"].ID,
		Type:        types.DepBlocks,
	}, "test-user")

	store.AddDependency(ctx, &types.Dependency{
		IssueID:     issues["B"].ID,
		DependsOnID: issues["C"].ID,
		Type:        types.DepBlocks,
	}, "test-user")

	// Try to close the cycle: C -> A (should fail)
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issues["C"].ID,
		DependsOnID: issues["A"].ID,
		Type:        types.DepBlocks,
	}, "test-user")
	if err == nil {
		t.Fatal("Expected AddDependency to fail when creating 3-node cycle")
	}

	// Verify no cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}
	if len(cycles) != 0 {
		t.Error("Expected no cycles since cycle was prevented")
	}
}

// TestDetectCycles_MultipleIndependentGraphs tests cycles in isolated subgraphs
func TestDetectCycles_MultipleGraphs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two independent dependency chains
	// Chain 1: A1 -> B1 -> C1
	// Chain 2: A2 -> B2 -> C2
	chains := [][]string{{"A1", "B1", "C1"}, {"A2", "B2", "C2"}}
	issuesMap := make(map[string]*types.Issue)

	for _, chain := range chains {
		for _, name := range chain {
			issue := &types.Issue{Title: name, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
			store.CreateIssue(ctx, issue, "test-user")
			issuesMap[name] = issue
		}

		// Link the chain
		for i := 0; i < len(chain)-1; i++ {
			store.AddDependency(ctx, &types.Dependency{
				IssueID:     issuesMap[chain[i]].ID,
				DependsOnID: issuesMap[chain[i+1]].ID,
				Type:        types.DepBlocks,
			}, "test-user")
		}
	}

	// Verify no cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}
	if len(cycles) != 0 {
		t.Errorf("Expected no cycles in independent chains, got %d", len(cycles))
	}
}

// TestDetectCycles_RelatesTypeAllowsBidirectionalWithoutCycleReport tests relates-to allows bidirectional links
// and DetectCycles correctly excludes them (they're "see also" links, not problematic cycles).
// This was fixed in GH#661 - relates-to is explicitly excluded from cycle detection.
func TestDetectCycles_RelatesTypeAllowsBidirectionalWithoutCycleReport(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two issues
	issueA := &types.Issue{Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issueA, "test-user")
	store.CreateIssue(ctx, issueB, "test-user")

	// Add A relates-to B
	err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issueA.ID,
		DependsOnID: issueB.ID,
		Type:        types.DepRelatesTo,
	}, "test-user")
	if err != nil {
		t.Fatalf("AddDependency for relates-to failed: %v", err)
	}

	// Add B relates-to A (this should succeed - relates-to skips cycle prevention)
	err = store.AddDependency(ctx, &types.Dependency{
		IssueID:     issueB.ID,
		DependsOnID: issueA.ID,
		Type:        types.DepRelatesTo,
	}, "test-user")
	if err != nil {
		t.Fatalf("AddDependency for reverse relates-to failed: %v", err)
	}

	// DetectCycles should NOT report relates-to as cycles (GH#661 fix)
	// relates-to is inherently bidirectional ("see also") and doesn't affect work ordering
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	// relates-to bidirectional should NOT be reported as a cycle
	if len(cycles) != 0 {
		t.Errorf("Expected 0 cycles for bidirectional relates-to (GH#661 fix), got %d", len(cycles))
	}

	// Verify both directions exist
	depsA, err := store.GetDependenciesWithMetadata(ctx, issueA.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}

	depsB, err := store.GetDependenciesWithMetadata(ctx, issueB.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}

	// A should have B as a dependency
	hasB := false
	for _, dep := range depsA {
		if dep.ID == issueB.ID && dep.DependencyType == types.DepRelatesTo {
			hasB = true
			break
		}
	}
	if !hasB {
		t.Error("Expected A to relate-to B")
	}

	// B should have A as a dependency
	hasA := false
	for _, dep := range depsB {
		if dep.ID == issueA.ID && dep.DependencyType == types.DepRelatesTo {
			hasA = true
			break
		}
	}
	if !hasA {
		t.Error("Expected B to relate-to A")
	}
}

// TestRemoveDependencyExternal verifies that removing an external dependency
// doesn't cause FK violation (bd-a3sj). External refs like external:project:capability
// don't exist in the issues table, so we must not mark them as dirty.
func TestRemoveDependencyExternal(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Issue with external dep",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add an external dependency
	externalRef := "external:other-project:some-capability"
	dep := &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: externalRef,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "test-user"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// This should NOT cause FK violation (the bug was marking external ref as dirty)
	err := store.RemoveDependency(ctx, issue.ID, externalRef, "test-user")
	if err != nil {
		t.Fatalf("RemoveDependency on external ref should succeed, got: %v", err)
	}

	// Verify dependency was actually removed
	deps, err := store.GetDependencyRecords(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("Expected 0 dependencies after removal, got %d", len(deps))
	}
}

// TestGetDependencyTreeExternalDeps verifies that external dependencies
// appear in the dependency tree as synthetic leaf nodes (bd-vks2).
func TestGetDependencyTreeExternalDeps(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a root issue
	root := &types.Issue{
		Title:     "Root issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, root, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Create a blocking local issue
	blocker := &types.Issue{
		Title:     "Local blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, blocker, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add local dependency
	localDep := &types.Dependency{
		IssueID:     root.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, localDep, "test-user"); err != nil {
		t.Fatalf("AddDependency (local) failed: %v", err)
	}

	// Add external dependency to root
	extRef := "external:test-project:test-capability"
	extDep := &types.Dependency{
		IssueID:     root.ID,
		DependsOnID: extRef,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, extDep, "test-user"); err != nil {
		t.Fatalf("AddDependency (external) failed: %v", err)
	}

	// Get dependency tree
	tree, err := store.GetDependencyTree(ctx, root.ID, 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}

	// Should have 3 nodes: root, local blocker, and external dep
	if len(tree) != 3 {
		t.Errorf("Expected 3 nodes in tree (root, local, external), got %d", len(tree))
		for _, node := range tree {
			t.Logf("Node: id=%s title=%s depth=%d", node.ID, node.Title, node.Depth)
		}
	}

	// Find the external dep node
	var extNode *types.TreeNode
	for _, node := range tree {
		if node.ID == extRef {
			extNode = node
			break
		}
	}

	if extNode == nil {
		t.Fatal("External dependency not found in tree")
	}

	// Verify external node properties
	if extNode.Depth != 1 {
		t.Errorf("Expected external dep at depth 1, got %d", extNode.Depth)
	}
	if extNode.ParentID != root.ID {
		t.Errorf("Expected external dep parent to be root, got %s", extNode.ParentID)
	}
	// External deps should show blocked status when not configured
	if extNode.Status != types.StatusBlocked {
		t.Errorf("Expected external dep status to be blocked (not configured), got %s", extNode.Status)
	}
}

// TestCycleDetectionWithExternalRefs verifies that external dependencies
// don't participate in cycle detection (they can't form cycles with local issues).
func TestCycleDetectionWithExternalRefs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create two issues
	issueA := &types.Issue{
		Title:     "Issue A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issueA, "test-user"); err != nil {
		t.Fatalf("CreateIssue A failed: %v", err)
	}

	issueB := &types.Issue{
		Title:     "Issue B",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issueB, "test-user"); err != nil {
		t.Fatalf("CreateIssue B failed: %v", err)
	}

	// A depends on B
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issueA.ID,
		DependsOnID: issueB.ID,
		Type:        types.DepBlocks,
	}, "test-user"); err != nil {
		t.Fatalf("AddDependency A->B failed: %v", err)
	}

	// B depends on external ref (should succeed - external refs don't form cycles)
	extRef := "external:project:capability"
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issueB.ID,
		DependsOnID: extRef,
		Type:        types.DepBlocks,
	}, "test-user"); err != nil {
		t.Fatalf("AddDependency B->external failed: %v", err)
	}

	// A depends on same external ref (should also succeed - no cycle with external)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     issueA.ID,
		DependsOnID: extRef,
		Type:        types.DepBlocks,
	}, "test-user"); err != nil {
		t.Fatalf("AddDependency A->external failed: %v", err)
	}

	// Verify DetectCycles doesn't find any cycles
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}
	if len(cycles) != 0 {
		t.Errorf("Expected no cycles with external deps, got %d", len(cycles))
	}
}
