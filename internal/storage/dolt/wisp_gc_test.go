package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestFindWispDependentsRecursive verifies that FindWispDependentsRecursive
// correctly discovers all transitive wisp dependents. This is the core logic
// for cascade-deleting blocked step children during wisp GC (bd-7hjy).
func TestFindWispDependentsRecursive(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a parent wisp (simulates a formula root)
	parent := &types.Issue{
		Title:     "parent formula wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, parent, "test"); err != nil {
		t.Fatalf("create parent wisp: %v", err)
	}

	// Create child wisps (simulate formula step wisps that depend on parent)
	child1 := &types.Issue{
		Title:     "step 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	child2 := &types.Issue{
		Title:     "step 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, child1, "test"); err != nil {
		t.Fatalf("create child1: %v", err)
	}
	if err := store.createWisp(ctx, child2, "test"); err != nil {
		t.Fatalf("create child2: %v", err)
	}

	// Create a grandchild (step that depends on child1)
	grandchild := &types.Issue{
		Title:     "substep of step 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, grandchild, "test"); err != nil {
		t.Fatalf("create grandchild: %v", err)
	}

	// Set up dependency links: children depend on parent, grandchild depends on child1
	deps := []*types.Dependency{
		{IssueID: child1.ID, DependsOnID: parent.ID, Type: types.DepBlocks},
		{IssueID: child2.ID, DependsOnID: parent.ID, Type: types.DepBlocks},
		{IssueID: grandchild.ID, DependsOnID: child1.ID, Type: types.DepBlocks},
	}
	for _, dep := range deps {
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	// Find all dependents starting from the parent
	discovered, err := store.FindWispDependentsRecursive(ctx, []string{parent.ID})
	if err != nil {
		t.Fatalf("FindWispDependentsRecursive: %v", err)
	}

	// Should discover child1, child2, and grandchild (3 dependents)
	if len(discovered) != 3 {
		t.Errorf("expected 3 dependents, got %d: %v", len(discovered), discovered)
	}
	for _, id := range []string{child1.ID, child2.ID, grandchild.ID} {
		if !discovered[id] {
			t.Errorf("expected dependent %s to be discovered", id)
		}
	}

	// Parent should NOT be in the discovered set (it was an input)
	if discovered[parent.ID] {
		t.Errorf("parent %s should not be in discovered set", parent.ID)
	}
}

// TestFindWispDependentsRecursive_Empty verifies empty input returns nil.
func TestFindWispDependentsRecursive_Empty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	discovered, err := store.FindWispDependentsRecursive(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if discovered != nil {
		t.Errorf("expected nil, got %v", discovered)
	}
}

// TestDeleteWispBatch_CleansUpDependencies verifies that deleteWispBatch
// removes wisp_dependencies rows where the deleted wisps appear as either
// issue_id or depends_on_id. This is the regression test for ff-tqm:
// a single OR query across both columns caused i/o timeouts on Dolt (slow
// union of two index scans inside a long-running mega-transaction); the fix
// uses two targeted DELETEs per batch, each hitting its own index, inside a
// per-batch transaction.
func TestDeleteWispBatch_CleansUpDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create three wisps: root, step-a (depends on root), step-b (depends on step-a)
	root := createTestWisp(t, ctx, store, "root wisp")
	stepA := createTestWisp(t, ctx, store, "step-a wisp")
	stepB := createTestWisp(t, ctx, store, "step-b wisp")

	// step-a blocked by root; step-b blocked by step-a
	mustAddWispDep(t, ctx, store, stepA.ID, root.ID)
	mustAddWispDep(t, ctx, store, stepB.ID, stepA.ID)

	// Delete all three in one batch — root appears as depends_on_id,
	// step-a appears as both issue_id and depends_on_id.
	deleted, err := store.deleteWispBatch(ctx, []string{root.ID, stepA.ID, stepB.ID})
	if err != nil {
		t.Fatalf("deleteWispBatch: %v", err)
	}
	if deleted != 3 {
		t.Errorf("expected 3 deleted, got %d", deleted)
	}

	// wisp_dependencies must be empty — no orphaned rows in either direction
	depCount := countWispDependencyRows(t, ctx, store.db, root.ID, stepA.ID, stepB.ID)
	if depCount != 0 {
		t.Errorf("expected 0 wisp_dependency rows after batch delete, got %d", depCount)
	}
}

// TestDeleteWispBatch_BothDirectionsCleared verifies that when a wisp appears
// as depends_on_id only (not issue_id) in wisp_dependencies, it is still
// removed. This is the exact failure mode of the pre-fix OR query vs two
// sequential DELETEs.
func TestDeleteWispBatch_BothDirectionsCleared(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// target is only referenced as depends_on_id; outsider is not being deleted
	target := createTestWisp(t, ctx, store, "target wisp")
	outsider := createTestWisp(t, ctx, store, "outsider wisp")

	// outsider depends on target
	mustAddWispDep(t, ctx, store, outsider.ID, target.ID)

	// Delete only target — the dep row where depends_on_id=target.ID must be removed
	deleted, err := store.deleteWispBatch(ctx, []string{target.ID})
	if err != nil {
		t.Fatalf("deleteWispBatch: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	depCount := countWispDependencyRows(t, ctx, store.db, target.ID, outsider.ID)
	if depCount != 0 {
		t.Errorf("expected 0 wisp_dependency rows referencing deleted wisp, got %d", depCount)
	}
}

// TestDeleteWispBatch_LargeBatch verifies that a batch exceeding the internal
// batchSize constant (200) is processed correctly across multiple transactions.
func TestDeleteWispBatch_LargeBatch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const count = 210 // exceeds batchSize=200 to exercise multi-batch path
	wisps := make([]*types.Issue, count)
	ids := make([]string, count)
	for i := range wisps {
		wisps[i] = createTestWisp(t, ctx, store, fmt.Sprintf("wisp-%d", i))
		ids[i] = wisps[i].ID
	}

	// Chain a dependency so the dep table is non-trivial
	mustAddWispDep(t, ctx, store, wisps[1].ID, wisps[0].ID)

	deleted, err := store.deleteWispBatch(ctx, ids)
	if err != nil {
		t.Fatalf("deleteWispBatch large batch: %v", err)
	}
	if deleted != count {
		t.Errorf("expected %d deleted, got %d", count, deleted)
	}
}

// --- helpers ---

func createTestWisp(t *testing.T, ctx context.Context, store *DoltStore, title string) *types.Issue {
	t.Helper()
	w := &types.Issue{
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, w, "test"); err != nil {
		t.Fatalf("createWisp %q: %v", title, err)
	}
	return w
}

func mustAddWispDep(t *testing.T, ctx context.Context, store *DoltStore, issueID, dependsOnID string) {
	t.Helper()
	dep := &types.Dependency{IssueID: issueID, DependsOnID: dependsOnID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency %s->%s: %v", issueID, dependsOnID, err)
	}
}

// countWispDependencyRows counts rows in wisp_dependencies that reference any
// of the given IDs (as either issue_id or depends_on_id).
func countWispDependencyRows(t *testing.T, ctx context.Context, db *sql.DB, ids ...string) int {
	t.Helper()
	if len(ids) == 0 {
		return 0
	}
	inClause, args := doltBuildSQLInClause(ids)
	//nolint:gosec // G201: inClause contains only ? markers
	query := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)",
		inClause, inClause,
	)
	var count int
	if err := db.QueryRowContext(ctx, query, append(args, args...)...).Scan(&count); err != nil {
		t.Fatalf("countWispDependencyRows: %v", err)
	}
	return count
}

// TestFindWispDependentsRecursive_NoDependents verifies wisps with no
// dependents return an empty map.
func TestFindWispDependentsRecursive_NoDependents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	wisp := &types.Issue{
		Title:     "lone wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, wisp, "test"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}

	discovered, err := store.FindWispDependentsRecursive(ctx, []string{wisp.ID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(discovered) != 0 {
		t.Errorf("expected 0 dependents, got %d: %v", len(discovered), discovered)
	}
}
