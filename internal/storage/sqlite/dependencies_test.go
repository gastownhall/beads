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
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create and link issues
	issue1 := &types.Issue{Title: "First", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Second", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")

	dep := &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepBlocks,
	}
	store.AddDependency(ctx, dep, "test-user")

	// Remove the dependency
	err := store.RemoveDependency(ctx, issue2.ID, issue1.ID, "test-user")
	if err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	// Verify dependency was removed
	deps, err := store.GetDependencies(ctx, issue2.ID)
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
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues: bd-2 and bd-3 both depend on bd-1
	issue1 := &types.Issue{Title: "Foundation", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Feature A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Feature B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	store.AddDependency(ctx, &types.Dependency{IssueID: issue2.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issue3.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")

	// Get dependents of issue1
	dependents, err := store.GetDependents(ctx, issue1.ID)
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
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a chain: bd-3 → bd-2 → bd-1
	issue1 := &types.Issue{Title: "Level 0", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Level 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Level 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	store.CreateIssue(ctx, issue1, "test-user")
	store.CreateIssue(ctx, issue2, "test-user")
	store.CreateIssue(ctx, issue3, "test-user")

	store.AddDependency(ctx, &types.Dependency{IssueID: issue2.ID, DependsOnID: issue1.ID, Type: types.DepBlocks}, "test-user")
	store.AddDependency(ctx, &types.Dependency{IssueID: issue3.ID, DependsOnID: issue2.ID, Type: types.DepBlocks}, "test-user")

	// Get tree starting from issue3
	tree, err := store.GetDependencyTree(ctx, issue3.ID, 10, false)
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
	tree, err := store.GetDependencyTree(ctx, issues[4].ID, 2, false)
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
	tree, err := store.GetDependencyTree(ctx, issue2.ID, 50, false)
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
	tree, err := store.GetDependencyTree(ctx, issue3.ID, 1, false)
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
	tree, err := store.GetDependencyTree(ctx, issues[9].ID, 10, false)
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
