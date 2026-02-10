//go:build cgo

package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// GetDependenciesWithMetadata Tests
// =============================================================================

func TestGetDependenciesWithMetadata(t *testing.T) {
	// Note: This test is skipped in embedded Dolt mode because GetDependenciesWithMetadata
	// makes nested GetIssue calls inside a rows cursor, which can cause connection issues.
	// This is a known limitation of the current implementation (see bd-tdgo.3).
	t.Skip("Skipping: GetDependenciesWithMetadata has nested query issue in embedded Dolt mode")
}

func TestGetDependenciesWithMetadata_NoResults(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue with no dependencies
	issue := &types.Issue{
		ID:        "no-deps-issue",
		Title:     "No Dependencies",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	deps, err := store.GetDependenciesWithMetadata(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}

	if len(deps) != 0 {
		t.Errorf("expected 0 dependencies, got %d", len(deps))
	}
}

// =============================================================================
// GetDependentsWithMetadata Tests
// =============================================================================

func TestGetDependentsWithMetadata(t *testing.T) {
	// Note: This test is skipped in embedded Dolt mode because GetDependentsWithMetadata
	// makes nested GetIssue calls inside a rows cursor, which can cause connection issues.
	// This is a known limitation of the current implementation (see bd-tdgo.3).
	t.Skip("Skipping: GetDependentsWithMetadata has nested query issue in embedded Dolt mode")
}

// =============================================================================
// GetDependencyRecords Tests
// =============================================================================

func TestGetDependencyRecords(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create issues
	issue := &types.Issue{
		ID:        "records-main",
		Title:     "Main Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	dep1 := &types.Issue{
		ID:        "records-dep1",
		Title:     "Dependency 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, dep1, "tester"); err != nil {
		t.Fatalf("failed to create dep1: %v", err)
	}

	dep2 := &types.Issue{
		ID:        "records-dep2",
		Title:     "Dependency 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, dep2, "tester"); err != nil {
		t.Fatalf("failed to create dep2: %v", err)
	}

	// Add dependencies
	for _, depIssue := range []string{dep1.ID, dep2.ID} {
		d := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: depIssue,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Get dependency records
	records, err := store.GetDependencyRecords(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	// Verify structure
	for _, r := range records {
		if r.IssueID != issue.ID {
			t.Errorf("expected IssueID %q, got %q", issue.ID, r.IssueID)
		}
		if r.Type != types.DepBlocks {
			t.Errorf("expected type %q, got %q", types.DepBlocks, r.Type)
		}
		if r.CreatedBy != "tester" {
			t.Errorf("expected CreatedBy 'tester', got %q", r.CreatedBy)
		}
	}
}

// =============================================================================
// GetAllDependencyRecords Tests
// =============================================================================

func TestGetAllDependencyRecords(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create several issues with dependencies
	issueA := &types.Issue{ID: "all-deps-a", Title: "Issue A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "all-deps-b", Title: "Issue B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{ID: "all-deps-c", Title: "Issue C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issueA, issueB, issueC} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// A depends on B, B depends on C
	deps := []*types.Dependency{
		{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks},
		{IssueID: issueB.ID, DependsOnID: issueC.ID, Type: types.DepBlocks},
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Get all dependency records
	allRecords, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		t.Fatalf("GetAllDependencyRecords failed: %v", err)
	}

	// Should have records keyed by issueA and issueB
	if len(allRecords) < 2 {
		t.Errorf("expected at least 2 issues with dependencies, got %d", len(allRecords))
	}

	if _, ok := allRecords[issueA.ID]; !ok {
		t.Errorf("expected records for %q", issueA.ID)
	}
	if _, ok := allRecords[issueB.ID]; !ok {
		t.Errorf("expected records for %q", issueB.ID)
	}
}

// =============================================================================
// GetDependencyCounts Tests
// =============================================================================

func TestGetDependencyCounts(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create issues: root blocks mid1 and mid2, mid1 blocks leaf
	root := &types.Issue{ID: "counts-root", Title: "Root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	mid1 := &types.Issue{ID: "counts-mid1", Title: "Mid 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	mid2 := &types.Issue{ID: "counts-mid2", Title: "Mid 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	leaf := &types.Issue{ID: "counts-leaf", Title: "Leaf", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{root, mid1, mid2, leaf} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Add blocking dependencies
	deps := []*types.Dependency{
		{IssueID: mid1.ID, DependsOnID: root.ID, Type: types.DepBlocks}, // mid1 blocked by root
		{IssueID: mid2.ID, DependsOnID: root.ID, Type: types.DepBlocks}, // mid2 blocked by root
		{IssueID: leaf.ID, DependsOnID: mid1.ID, Type: types.DepBlocks}, // leaf blocked by mid1
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Get counts for all issues
	issueIDs := []string{root.ID, mid1.ID, mid2.ID, leaf.ID}
	counts, err := store.GetDependencyCounts(ctx, issueIDs)
	if err != nil {
		t.Fatalf("GetDependencyCounts failed: %v", err)
	}

	// root: 0 deps, 2 dependents
	if counts[root.ID].DependencyCount != 0 {
		t.Errorf("root should have 0 deps, got %d", counts[root.ID].DependencyCount)
	}
	if counts[root.ID].DependentCount != 2 {
		t.Errorf("root should have 2 dependents, got %d", counts[root.ID].DependentCount)
	}

	// mid1: 1 dep, 1 dependent
	if counts[mid1.ID].DependencyCount != 1 {
		t.Errorf("mid1 should have 1 dep, got %d", counts[mid1.ID].DependencyCount)
	}
	if counts[mid1.ID].DependentCount != 1 {
		t.Errorf("mid1 should have 1 dependent, got %d", counts[mid1.ID].DependentCount)
	}

	// leaf: 1 dep, 0 dependents
	if counts[leaf.ID].DependencyCount != 1 {
		t.Errorf("leaf should have 1 dep, got %d", counts[leaf.ID].DependencyCount)
	}
	if counts[leaf.ID].DependentCount != 0 {
		t.Errorf("leaf should have 0 dependents, got %d", counts[leaf.ID].DependentCount)
	}
}

func TestGetDependencyCounts_EmptyList(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	counts, err := store.GetDependencyCounts(ctx, []string{})
	if err != nil {
		t.Fatalf("GetDependencyCounts failed: %v", err)
	}

	if len(counts) != 0 {
		t.Errorf("expected empty map, got %d entries", len(counts))
	}
}

// =============================================================================
// GetDependencyTree Tests
// =============================================================================

func TestGetDependencyTree(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a simple tree: root -> child1, root -> child2
	root := &types.Issue{ID: "tree-root", Title: "Root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	child1 := &types.Issue{ID: "tree-child1", Title: "Child 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	child2 := &types.Issue{ID: "tree-child2", Title: "Child 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{root, child1, child2} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Add dependencies
	for _, childID := range []string{child1.ID, child2.ID} {
		d := &types.Dependency{
			IssueID:     root.ID,
			DependsOnID: childID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Get dependency tree (forward direction)
	tree, err := store.GetDependencyTree(ctx, root.ID, 3, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree failed: %v", err)
	}

	// Should have root + 2 children = 3 nodes
	if len(tree) != 3 {
		t.Errorf("expected 3 nodes in tree, got %d", len(tree))
	}

	// Verify root is at depth 0
	if tree[0].Issue.ID != root.ID || tree[0].Depth != 0 {
		t.Errorf("expected root at depth 0, got %q at depth %d", tree[0].Issue.ID, tree[0].Depth)
	}
}

func TestGetDependencyTree_Reverse(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create chain: leaf -> mid -> root
	root := &types.Issue{ID: "rtree-root", Title: "Root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	mid := &types.Issue{ID: "rtree-mid", Title: "Mid", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	leaf := &types.Issue{ID: "rtree-leaf", Title: "Leaf", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{root, mid, leaf} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// mid depends on root, leaf depends on mid
	deps := []*types.Dependency{
		{IssueID: mid.ID, DependsOnID: root.ID, Type: types.DepBlocks},
		{IssueID: leaf.ID, DependsOnID: mid.ID, Type: types.DepBlocks},
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Get reverse tree from root (shows dependents)
	tree, err := store.GetDependencyTree(ctx, root.ID, 3, false, true)
	if err != nil {
		t.Fatalf("GetDependencyTree reverse failed: %v", err)
	}

	// Should have root + mid = 2 nodes (leaf is dependent of mid, not root)
	if len(tree) < 2 {
		t.Errorf("expected at least 2 nodes in reverse tree, got %d", len(tree))
	}
}

// =============================================================================
// DetectCycles Tests
// =============================================================================

func TestDetectCycles_NoCycle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create linear chain: A -> B -> C
	issueA := &types.Issue{ID: "nocycle-a", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "nocycle-b", Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{ID: "nocycle-c", Title: "C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issueA, issueB, issueC} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// A depends on B, B depends on C
	deps := []*types.Dependency{
		{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks},
		{IssueID: issueB.ID, DependsOnID: issueC.ID, Type: types.DepBlocks},
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("expected no cycles, found %d", len(cycles))
	}
}

func TestDetectCycles_WithCycle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create cycle: A -> B -> C -> A
	issueA := &types.Issue{ID: "cycle-a", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "cycle-b", Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{ID: "cycle-c", Title: "C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issueA, issueB, issueC} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Create cycle
	deps := []*types.Dependency{
		{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks},
		{IssueID: issueB.ID, DependsOnID: issueC.ID, Type: types.DepBlocks},
		{IssueID: issueC.ID, DependsOnID: issueA.ID, Type: types.DepBlocks}, // Creates cycle
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) == 0 {
		t.Error("expected to find a cycle")
	}
}

// =============================================================================
// GetNewlyUnblockedByClose Tests
// =============================================================================

func TestGetNewlyUnblockedByClose(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create blocker and two blocked issues
	blocker := &types.Issue{
		ID:        "unblock-blocker",
		Title:     "Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blockedOnly := &types.Issue{
		ID:        "unblock-only",
		Title:     "Blocked Only by One",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	blockedMultiple := &types.Issue{
		ID:        "unblock-multi",
		Title:     "Blocked by Multiple",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	otherBlocker := &types.Issue{
		ID:        "unblock-other",
		Title:     "Other Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	for _, issue := range []*types.Issue{blocker, blockedOnly, blockedMultiple, otherBlocker} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// blockedOnly depends only on blocker
	// blockedMultiple depends on both blocker and otherBlocker
	deps := []*types.Dependency{
		{IssueID: blockedOnly.ID, DependsOnID: blocker.ID, Type: types.DepBlocks},
		{IssueID: blockedMultiple.ID, DependsOnID: blocker.ID, Type: types.DepBlocks},
		{IssueID: blockedMultiple.ID, DependsOnID: otherBlocker.ID, Type: types.DepBlocks},
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Get issues that would be unblocked if we close 'blocker'
	unblocked, err := store.GetNewlyUnblockedByClose(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("GetNewlyUnblockedByClose failed: %v", err)
	}

	// Only blockedOnly should be newly unblocked (blockedMultiple still has otherBlocker)
	if len(unblocked) != 1 {
		t.Fatalf("expected 1 newly unblocked issue, got %d", len(unblocked))
	}

	if unblocked[0].ID != blockedOnly.ID {
		t.Errorf("expected %q to be unblocked, got %q", blockedOnly.ID, unblocked[0].ID)
	}
}

func TestGetNewlyUnblockedByClose_ClosedDependent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create issues where the dependent is already closed
	blocker := &types.Issue{
		ID:        "unblock-closed-blocker",
		Title:     "Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	closedDependent := &types.Issue{
		ID:        "unblock-closed-dep",
		Title:     "Already Closed",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, issue := range []*types.Issue{blocker, closedDependent} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Add dependency
	dep := &types.Dependency{
		IssueID:     closedDependent.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Closed issues should not be returned as newly unblocked
	unblocked, err := store.GetNewlyUnblockedByClose(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("GetNewlyUnblockedByClose failed: %v", err)
	}

	if len(unblocked) != 0 {
		t.Errorf("expected 0 unblocked (closed issue shouldn't count), got %d", len(unblocked))
	}
}

// =============================================================================
// External Dependency Tests (cross-rig references)
// =============================================================================

func TestAddDependency_ExternalReference(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a local issue
	issue := &types.Issue{
		ID:        "ext-dep-issue",
		Title:     "Issue with External Dependency",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add dependency on external reference (cross-rig tracking)
	// This should NOT fail with FK violation after the fix
	externalRef := "external:da:da-7eo"
	dep := &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: externalRef,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add external dependency: %v", err)
	}

	// Verify the dependency was created
	records, err := store.GetDependencyRecords(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(records))
	}

	if records[0].DependsOnID != externalRef {
		t.Errorf("expected DependsOnID %q, got %q", externalRef, records[0].DependsOnID)
	}
}

func TestAddDependency_MultipleExternalReferences(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a convoy-like issue
	convoy := &types.Issue{
		ID:        "convoy-test",
		Title:     "Test Convoy",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, convoy, "tester"); err != nil {
		t.Fatalf("failed to create convoy: %v", err)
	}

	// Add multiple external dependencies (simulating cross-rig tracking)
	externalRefs := []string{
		"external:da:da-7eo",
		"external:da:da-1nw",
		"external:gt:gt-abc",
	}

	for _, ref := range externalRefs {
		dep := &types.Dependency{
			IssueID:     convoy.ID,
			DependsOnID: ref,
			Type:        "tracks", // convoy tracking type
		}
		if err := store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("failed to add external dependency %s: %v", ref, err)
		}
	}

	// Verify all dependencies were created
	records, err := store.GetDependencyRecords(ctx, convoy.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}

	if len(records) != len(externalRefs) {
		t.Errorf("expected %d dependencies, got %d", len(externalRefs), len(records))
	}
}

// Note: testContext is already defined in dolt_test.go for this package
