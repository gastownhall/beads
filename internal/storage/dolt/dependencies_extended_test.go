package dolt

import (
	"strings"
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
	root := &types.Issue{ID: "tree-root", Title: "Root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
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

	// First two deps succeed
	dep1 := &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep1, "tester"); err != nil {
		t.Fatalf("failed to add dependency A->B: %v", err)
	}
	dep2 := &types.Dependency{IssueID: issueB.ID, DependsOnID: issueC.ID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep2, "tester"); err != nil {
		t.Fatalf("failed to add dependency B->C: %v", err)
	}

	// Third dep would create cycle - should be rejected
	dep3 := &types.Dependency{IssueID: issueC.ID, DependsOnID: issueA.ID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep3, "tester"); err == nil {
		t.Fatal("expected AddDependency to fail when creating cycle, but it succeeded")
	}

	// Since cycle was prevented, DetectCycles should find nothing
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}

	if len(cycles) != 0 {
		t.Errorf("expected no cycles since cycle was prevented, got %d", len(cycles))
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
// Custom Status Visibility Tests (bd-1x0)
// =============================================================================

// TestIsBlocked_CustomStatusBlocker verifies that a blocker with a custom status
// still counts as blocking (not invisible like it was before bd-1x0).
func TestIsBlocked_CustomStatusBlocker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Configure a custom status
	if err := store.SetConfig(ctx, "status.custom", "review"); err != nil {
		t.Fatalf("failed to set custom status config: %v", err)
	}

	// Create a blocker with custom status 'review'
	blocker := &types.Issue{
		ID:        "custom-blocker",
		Title:     "Blocker in Review",
		Status:    types.Status("review"),
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blocked := &types.Issue{
		ID:        "custom-blocked",
		Title:     "Blocked by Custom Status",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	for _, issue := range []*types.Issue{blocker, blocked} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: blocked.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// The blocker has custom status 'review' — it should still count as active
	isBlocked, blockers, err := store.IsBlocked(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("IsBlocked failed: %v", err)
	}
	if !isBlocked {
		t.Error("issue should be blocked by custom-status blocker, but IsBlocked returned false")
	}
	if len(blockers) == 0 {
		t.Error("expected blocker IDs, got empty list")
	}
}

// TestGetNewlyUnblockedByClose_CustomStatusCandidate verifies that an issue
// with a custom status can appear as a newly-unblocked candidate.
func TestGetNewlyUnblockedByClose_CustomStatusCandidate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Configure a custom status
	if err := store.SetConfig(ctx, "status.custom", "awaiting_review"); err != nil {
		t.Fatalf("failed to set custom status config: %v", err)
	}

	blocker := &types.Issue{
		ID:        "cs-unblock-blocker",
		Title:     "Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	candidate := &types.Issue{
		ID:        "cs-unblock-candidate",
		Title:     "Custom Status Candidate",
		Status:    types.Status("awaiting_review"),
		Priority:  2,
		IssueType: types.TypeTask,
	}
	for _, issue := range []*types.Issue{blocker, candidate} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: candidate.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Close the blocker — candidate with custom status should appear as unblocked
	unblocked, err := store.GetNewlyUnblockedByClose(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("GetNewlyUnblockedByClose failed: %v", err)
	}
	if len(unblocked) != 1 {
		t.Fatalf("expected 1 newly unblocked issue, got %d", len(unblocked))
	}
	if unblocked[0].ID != candidate.ID {
		t.Errorf("expected %q to be unblocked, got %q", candidate.ID, unblocked[0].ID)
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

// =============================================================================
// Cross-Prefix Dependency Tests
// =============================================================================

func TestIsCrossPrefixDep(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		target   string
		expected bool
	}{
		{"same prefix", "sh-abc", "sh-def", false},
		{"different prefix", "sh-abc", "hq-def", true},
		{"hq to bd", "hq-abc", "bd-def", true},
		{"same prefix with subtype", "hq-cv-abc", "hq-xyz", false},
		{"no prefix source", "abc", "sh-def", true},
		{"no prefix either", "abc", "def", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCrossPrefixDep(tt.source, tt.target)
			if got != tt.expected {
				t.Errorf("isCrossPrefixDep(%q, %q) = %v, want %v", tt.source, tt.target, got, tt.expected)
			}
		})
	}
}

func TestAddDependency_CrossPrefix_SkipsTargetExistence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a source issue with prefix "test-"
	source := &types.Issue{
		ID:        "test-source",
		Title:     "Source Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, source, "tester"); err != nil {
		t.Fatalf("failed to create source issue: %v", err)
	}

	// Add a cross-prefix dependency — target "other-xyz" doesn't exist in this DB,
	// but should succeed because cross-prefix deps skip target existence check.
	dep := &types.Dependency{
		IssueID:     "test-source",
		DependsOnID: "other-xyz",
		Type:        types.DepBlocks,
	}
	err := store.AddDependency(ctx, dep, "tester")
	if err != nil {
		t.Fatalf("AddDependency should succeed for cross-prefix dep, got: %v", err)
	}

	// Verify the dependency was stored
	records, err := store.GetDependencyRecords(ctx, "test-source")
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}
	found := false
	for _, r := range records {
		if r.DependsOnID == "other-xyz" {
			found = true
			break
		}
	}
	if !found {
		t.Error("cross-prefix dependency was not stored")
	}
}

func TestAddDependency_SamePrefix_RequiresTargetExistence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a source issue
	source := &types.Issue{
		ID:        "test-source2",
		Title:     "Source Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, source, "tester"); err != nil {
		t.Fatalf("failed to create source issue: %v", err)
	}

	// Same-prefix dep with non-existent target should fail
	dep := &types.Dependency{
		IssueID:     "test-source2",
		DependsOnID: "test-nonexistent",
		Type:        types.DepBlocks,
	}
	err := store.AddDependency(ctx, dep, "tester")
	if err == nil {
		t.Fatal("AddDependency should fail for same-prefix dep with non-existent target")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// =============================================================================
// Cross-Type Blocking Validation Tests (GH#1495)
// =============================================================================

func TestAddDependency_BlocksCrossType_TaskBlocksEpic(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	task := &types.Issue{
		ID:        "ct-task-1",
		Title:     "A task",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	epic := &types.Issue{
		ID:        "ct-epic-1",
		Title:     "An epic",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, task, "tester"); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := store.CreateIssue(ctx, epic, "tester"); err != nil {
		t.Fatalf("failed to create epic: %v", err)
	}

	// Task blocks epic -> should fail
	dep := &types.Dependency{
		IssueID:     "ct-epic-1",
		DependsOnID: "ct-task-1",
		Type:        types.DepBlocks,
	}
	err := store.AddDependency(ctx, dep, "tester")
	if err == nil {
		t.Fatal("expected error when task blocks epic, got nil")
	}
	if !strings.Contains(err.Error(), "can only block") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAddDependency_BlocksCrossType_EpicBlocksTask(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	task := &types.Issue{
		ID:        "ct-task-2",
		Title:     "A task",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	epic := &types.Issue{
		ID:        "ct-epic-2",
		Title:     "An epic",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, task, "tester"); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := store.CreateIssue(ctx, epic, "tester"); err != nil {
		t.Fatalf("failed to create epic: %v", err)
	}

	// Epic blocks task -> should fail
	dep := &types.Dependency{
		IssueID:     "ct-task-2",
		DependsOnID: "ct-epic-2",
		Type:        types.DepBlocks,
	}
	err := store.AddDependency(ctx, dep, "tester")
	if err == nil {
		t.Fatal("expected error when epic blocks task, got nil")
	}
	if !strings.Contains(err.Error(), "can only block") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAddDependency_BlocksSameType_TaskBlocksTask(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	task1 := &types.Issue{
		ID:        "ct-task-3a",
		Title:     "Task A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	task2 := &types.Issue{
		ID:        "ct-task-3b",
		Title:     "Task B",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, task1, "tester"); err != nil {
		t.Fatalf("failed to create task1: %v", err)
	}
	if err := store.CreateIssue(ctx, task2, "tester"); err != nil {
		t.Fatalf("failed to create task2: %v", err)
	}

	// Task blocks task -> should succeed
	dep := &types.Dependency{
		IssueID:     "ct-task-3b",
		DependsOnID: "ct-task-3a",
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("task blocking task should succeed: %v", err)
	}
}

func TestAddDependency_BlocksSameType_EpicBlocksEpic(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	epic1 := &types.Issue{
		ID:        "ct-epic-4a",
		Title:     "Epic A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	epic2 := &types.Issue{
		ID:        "ct-epic-4b",
		Title:     "Epic B",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, epic1, "tester"); err != nil {
		t.Fatalf("failed to create epic1: %v", err)
	}
	if err := store.CreateIssue(ctx, epic2, "tester"); err != nil {
		t.Fatalf("failed to create epic2: %v", err)
	}

	// Epic blocks epic -> should succeed
	dep := &types.Dependency{
		IssueID:     "ct-epic-4b",
		DependsOnID: "ct-epic-4a",
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("epic blocking epic should succeed: %v", err)
	}
}

func TestAddDependency_ParentChild_CrossType_Allowed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	task := &types.Issue{
		ID:        "ct-task-5",
		Title:     "A task",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	epic := &types.Issue{
		ID:        "ct-epic-5",
		Title:     "An epic",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, task, "tester"); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := store.CreateIssue(ctx, epic, "tester"); err != nil {
		t.Fatalf("failed to create epic: %v", err)
	}

	// Parent-child between epic and task -> should succeed (only blocks is restricted)
	dep := &types.Dependency{
		IssueID:     "ct-task-5",
		DependsOnID: "ct-epic-5",
		Type:        types.DepParentChild,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("parent-child cross-type should succeed: %v", err)
	}
}

// =============================================================================
// Self-Dependency Rejection Tests (bd-2qr)
// =============================================================================

func TestAddDependency_SelfDependencyRejected(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "self-dep-issue",
		Title:     "Self Dependency Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Try to add a dependency from the issue to itself
	dep := &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: issue.ID,
		Type:        types.DepBlocks,
	}
	err := store.AddDependency(ctx, dep, "tester")
	if err == nil {
		t.Fatal("expected AddDependency to reject self-dependency, but it succeeded")
	}
	if !strings.Contains(err.Error(), "self-dependency") {
		t.Errorf("expected error containing 'self-dependency', got: %v", err)
	}
}

// =============================================================================
// Conditional-Blocks Cycle Detection Tests
// =============================================================================

func TestDetectCycles_ConditionalBlocksCycle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Conditional-blocks should also be detected as cycles
	issueA := &types.Issue{ID: "cb-cycle-a", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "cb-cycle-b", Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issueA, issueB} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// A conditional-blocks B
	dep1 := &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepConditionalBlocks}
	if err := store.AddDependency(ctx, dep1, "tester"); err != nil {
		t.Fatalf("failed to add dependency A->B: %v", err)
	}

	// B conditional-blocks A would create cycle — should be rejected
	dep2 := &types.Dependency{IssueID: issueB.ID, DependsOnID: issueA.ID, Type: types.DepConditionalBlocks}
	err := store.AddDependency(ctx, dep2, "tester")
	if err == nil {
		t.Fatal("expected AddDependency to reject conditional-blocks cycle, but it succeeded")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected error containing 'cycle', got: %v", err)
	}
}

func TestDetectCycles_MixedBlocksAndConditionalBlocks(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Mixed: A blocks B, B conditional-blocks C, C blocks A = cycle
	issueA := &types.Issue{ID: "mixed-cycle-a", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "mixed-cycle-b", Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueC := &types.Issue{ID: "mixed-cycle-c", Title: "C", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issueA, issueB, issueC} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep1 := &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep1, "tester"); err != nil {
		t.Fatalf("failed to add A->B blocks: %v", err)
	}

	dep2 := &types.Dependency{IssueID: issueB.ID, DependsOnID: issueC.ID, Type: types.DepConditionalBlocks}
	if err := store.AddDependency(ctx, dep2, "tester"); err != nil {
		t.Fatalf("failed to add B->C conditional-blocks: %v", err)
	}

	// C->A would close the cycle through mixed types
	dep3 := &types.Dependency{IssueID: issueC.ID, DependsOnID: issueA.ID, Type: types.DepBlocks}
	err := store.AddDependency(ctx, dep3, "tester")
	if err == nil {
		t.Fatal("expected AddDependency to reject mixed blocks/conditional-blocks cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected error containing 'cycle', got: %v", err)
	}
}

func TestDetectCycles_WaitsForDoesNotCreateCycle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// waits-for should NOT trigger cycle detection (gate semantics)
	issueA := &types.Issue{ID: "wf-nocycle-a", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "wf-nocycle-b", Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issueA, issueB} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// A waits-for B
	dep1 := &types.Dependency{IssueID: issueA.ID, DependsOnID: issueB.ID, Type: types.DepWaitsFor}
	if err := store.AddDependency(ctx, dep1, "tester"); err != nil {
		t.Fatalf("failed to add A waits-for B: %v", err)
	}

	// B waits-for A should succeed (waits-for doesn't create blocking cycles)
	dep2 := &types.Dependency{IssueID: issueB.ID, DependsOnID: issueA.ID, Type: types.DepWaitsFor}
	if err := store.AddDependency(ctx, dep2, "tester"); err != nil {
		t.Fatalf("expected waits-for cycle to be allowed, got error: %v", err)
	}

	// DetectCycles should find nothing (waits-for excluded from cycle detection)
	cycles, err := store.DetectCycles(ctx)
	if err != nil {
		t.Fatalf("DetectCycles failed: %v", err)
	}
	if len(cycles) != 0 {
		t.Errorf("expected no cycles (waits-for excluded), found %d", len(cycles))
	}
}

func TestAddDependency_SelfDependencyAllTypes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "self-dep-all",
		Title:     "Self Dep All Types",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Self-dependency should be rejected for all dependency types
	for _, depType := range []types.DependencyType{
		types.DepBlocks,
		types.DepConditionalBlocks,
		types.DepWaitsFor,
	} {
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: issue.ID,
			Type:        depType,
		}
		err := store.AddDependency(ctx, dep, "tester")
		if err == nil {
			t.Errorf("expected self-dependency to be rejected for type %q", depType)
		}
		if !strings.Contains(err.Error(), "self-dependency") {
			t.Errorf("expected 'self-dependency' in error for type %q, got: %v", depType, err)
		}
	}
}

// =============================================================================
// Ready Work + Blocked Issues Integration Tests
// =============================================================================

func TestGetReadyWork_WithConditionalBlocks(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// A conditional-blocks B: B should be blocked, A should be ready
	issueA := &types.Issue{ID: "cb-ready-a", Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "cb-ready-b", Title: "Blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issueA, issueB} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep := &types.Dependency{IssueID: issueB.ID, DependsOnID: issueA.ID, Type: types.DepConditionalBlocks}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	readyIssues, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// A should be ready, B should not
	readyIDs := make(map[string]bool)
	for _, issue := range readyIssues {
		readyIDs[issue.ID] = true
	}

	if !readyIDs["cb-ready-a"] {
		t.Error("expected issue A to be ready")
	}
	if readyIDs["cb-ready-b"] {
		t.Error("expected issue B to be blocked, but it appeared in ready work")
	}
}

func TestGetBlockedIssues_WithBlockerDetails(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker := &types.Issue{ID: "blk-detail-a", Title: "The Blocker", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeBug}
	blocked := &types.Issue{ID: "blk-detail-b", Title: "Waiting on fix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{blocker, blocked} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep := &types.Dependency{IssueID: blocked.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	blockedIssues, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed: %v", err)
	}

	var found *types.BlockedIssue
	for _, bi := range blockedIssues {
		if bi.ID == "blk-detail-b" {
			found = bi
			break
		}
	}
	if found == nil {
		t.Fatal("expected blocked issue 'blk-detail-b' not found")
	}
	if found.BlockedByCount < 1 {
		t.Errorf("expected BlockedByCount >= 1, got %d", found.BlockedByCount)
	}
	if len(found.BlockedBy) == 0 {
		t.Fatal("expected BlockedBy to contain blocker ID")
	}
	if found.BlockedBy[0] != "blk-detail-a" {
		t.Errorf("expected BlockedBy[0] = 'blk-detail-a', got %q", found.BlockedBy[0])
	}
}

// Note: testContext is already defined in dolt_test.go for this package
