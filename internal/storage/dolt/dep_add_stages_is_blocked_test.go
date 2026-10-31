package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestAddDependency_StagesIsBlockedFlip is the GH#6647 regression: under
// auto-commit, the is_blocked flip AddDependency writes to the source's issues
// row must ride the same Dolt commit as the edge, leaving the graph tables
// clean so a later RecomputeAllBlocked is not refused.
func TestAddDependency_StagesIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, false) // two open issues, clean committed tree
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: graph tables must be clean after seed, got dirty=%v", dirty)
	}

	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bm-w", DependsOnID: "bm-x", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatalf("precondition: bm-w must be blocked in the working set")
	}

	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("dep add must leave graph tables clean (is_blocked committed with the edge), got dirty=%v", dirty)
	}
	if !headIsBlocked(ctx, t, store, "bm-w") {
		t.Fatalf("committed HEAD has bm-w is_blocked=0 despite an open blocker")
	}
	if _, err := store.RecomputeAllBlocked(ctx); err != nil {
		t.Fatalf("RecomputeAllBlocked after dep add: %v", err)
	}
}

// TestRemoveDependency_StagesIsBlockedClear is the store-verb removal half of
// GH#6647: removing the only blocker clears is_blocked in the removal's own
// commit.
func TestRemoveDependency_StagesIsBlockedClear(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true) // edge and flip committed at HEAD
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: graph tables must be clean after seed, got dirty=%v", dirty)
	}
	if !headIsBlocked(ctx, t, store, "bm-w") {
		t.Fatal("precondition: HEAD must have bm-w blocked")
	}

	if err := store.RemoveDependency(ctx, "bm-w", "bm-x", "tester"); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}

	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("dep remove must leave graph tables clean (is_blocked committed with the removal), got dirty=%v", dirty)
	}
	if headIsBlocked(ctx, t, store, "bm-w") {
		t.Fatal("committed HEAD still has bm-w is_blocked=1 after its only blocker was removed")
	}
}

// TestAddWispDependency_StagesDurableIsBlockedFlip is GH#6647 on the wisp
// arms of the store verbs: the wisp tables are dolt-ignored, but a blocking
// edge from a wisp parent still flips its durable child's is_blocked, and that
// issues row must be committed (and cleared again on removal) rather than left
// in the working set.
func TestAddWispDependency_StagesDurableIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedWispParentOfDurableChild(ctx, t, store, "wbm-w", "wbm-d", "wbm-a")

	if err := store.AddDependencyWithOptions(ctx, &types.Dependency{IssueID: "wbm-w", DependsOnID: "wbm-a", Type: types.DepBlocks}, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
		t.Fatalf("AddDependencyWithOptions: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "wbm-d") {
		t.Fatal("precondition: wbm-d must be blocked in the working set through its blocked wisp parent")
	}
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("wisp dep add must leave graph tables clean (durable is_blocked committed), got dirty=%v", dirty)
	}
	if !headIsBlocked(ctx, t, store, "wbm-d") {
		t.Fatal("committed HEAD has wbm-d is_blocked=0 despite its blocked wisp parent")
	}

	if err := store.RemoveDependencyWithOptions(ctx, "wbm-w", "wbm-a", "tester", storage.DependencyRemoveOptions{EmitEvent: true}); err != nil {
		t.Fatalf("RemoveDependencyWithOptions: %v", err)
	}
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("wisp dep remove must leave graph tables clean (durable is_blocked committed), got dirty=%v", dirty)
	}
	if headIsBlocked(ctx, t, store, "wbm-d") {
		t.Fatal("committed HEAD still has wbm-d is_blocked=1 after its wisp parent's only blocker was removed")
	}
}

// TestRunInTransactionDependency_StagesIsBlockedFlip is GH#6647 on the
// explicit-transaction dep verbs that `bd batch` dep lines and the molecule
// writer use: the is_blocked flip is staged with the edge, so the
// transaction's own Dolt commit carries it, for the add and for the removal.
func TestRunInTransactionDependency_StagesIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, false) // two open issues, clean committed tree
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: graph tables must be clean after seed, got dirty=%v", dirty)
	}

	if err := store.RunInTransaction(ctx, "test: tx dep add", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{IssueID: "bm-w", DependsOnID: "bm-x", Type: types.DepBlocks}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction AddDependency: %v", err)
	}
	requireCommittedIsBlocked(ctx, t, store, "tx dep add", "bm-w", true)

	if err := store.RunInTransaction(ctx, "test: tx dep remove", func(tx storage.Transaction) error {
		return tx.RemoveDependency(ctx, "bm-w", "bm-x", "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction RemoveDependency: %v", err)
	}
	requireCommittedIsBlocked(ctx, t, store, "tx dep remove", "bm-w", false)
}

// TestRunInTransactionWispDependency_StagesDurableIsBlockedFlip is the
// wisp-source half on the explicit-transaction verbs. With the events journal
// off, RunInTransaction writes a wisp-sourced edge on its dolt-ignored
// session, and that session commits after the transaction's Dolt commit, so
// the durable child the edge flips must still reach HEAD rather than land in
// the working set after the commit was cut.
func TestRunInTransactionWispDependency_StagesDurableIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedWispParentOfDurableChild(ctx, t, store, "twbm-w", "twbm-d", "twbm-a")

	if err := store.RunInTransaction(ctx, "test: tx wisp dep add", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{IssueID: "twbm-w", DependsOnID: "twbm-a", Type: types.DepBlocks}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction AddDependency: %v", err)
	}
	requireCommittedIsBlocked(ctx, t, store, "tx wisp dep add", "twbm-d", true)

	if err := store.RunInTransaction(ctx, "test: tx wisp dep remove", func(tx storage.Transaction) error {
		return tx.RemoveDependency(ctx, "twbm-w", "twbm-a", "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction RemoveDependency: %v", err)
	}
	requireCommittedIsBlocked(ctx, t, store, "tx wisp dep remove", "twbm-d", false)
}

// TestBatchApplierDepAdd_StagesIsBlockedFlip is GH#6647 on BatchApplier's
// dep_add item: a request whose only change is a blocking edge commits the
// source's is_blocked with the edge.
func TestBatchApplierDepAdd_StagesIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, false) // two open issues, clean committed tree
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: graph tables must be clean after seed, got dirty=%v", dirty)
	}

	applier, err := store.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	if _, err := applier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor: "tester",
		Items: []publicops.ApplyItem{{
			Kind: publicops.ItemDepAdd,
			DepAdd: &publicops.DepAddItem{
				Source: publicops.Ref{ID: "bm-w"},
				Target: publicops.Ref{ID: "bm-x"},
				Type:   publicops.DepBlocks,
			},
		}},
	}); err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	requireCommittedIsBlocked(ctx, t, store, "batch dep_add", "bm-w", true)
}

// TestCreateWispChildOfDurableSpawner_StagesWaiterIsBlockedFlip is GH#6647 on
// a wisp batch create: a wisp created with a parent-child edge to a durable
// spawner blocks the spawner's durable waits-for waiter, and that issues row
// must be committed, on the store's all-wisps create path and on a
// journal-off transaction, which creates wisps on its dolt-ignored session.
func TestCreateWispChildOfDurableSpawner_StagesWaiterIsBlockedFlip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, test := range []struct {
		name   string
		create func(wisp *types.Issue) error
	}{
		{
			name: "store",
			create: func(wisp *types.Issue) error {
				return store.CreateIssuesWithFullOptions(ctx, []*types.Issue{wisp}, "tester",
					storage.BatchCreateOptions{SkipPrefixValidation: true})
			},
		},
		{
			name: "tx",
			create: func(wisp *types.Issue) error {
				return store.RunInTransaction(ctx, "test: tx create wisp child", func(tx storage.Transaction) error {
					return tx.CreateIssues(ctx, []*types.Issue{wisp}, "tester")
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spawner := "wsc-" + test.name + "-spawner"
			waiter := "wsc-" + test.name + "-waiter"
			createPerm(t, ctx, store, spawner)
			createPerm(t, ctx, store, waiter)
			if err := store.AddDependency(ctx, &types.Dependency{
				IssueID: waiter, DependsOnID: spawner, Type: types.DepWaitsFor, Metadata: `{"gate":"all-children"}`,
			}, "tester"); err != nil {
				t.Fatalf("seed waits-for: %v", err)
			}
			if err := store.Commit(ctx, "test: seed spawner and waiter"); err != nil {
				t.Fatalf("commit seed: %v", err)
			}
			requireCommittedIsBlocked(ctx, t, store, "seed", waiter, false)

			if err := test.create(&types.Issue{
				ID: "wsc-" + test.name + "-wisp", Title: "wisp child", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
				Dependencies: []*types.Dependency{{DependsOnID: spawner, Type: types.DepParentChild}},
			}); err != nil {
				t.Fatalf("create wisp child: %v", err)
			}
			if !isBlocked(ctx, t, store.db, waiter) {
				t.Fatalf("precondition: %s must be blocked in the working set by its spawner's open wisp child", waiter)
			}
			requireCommittedIsBlocked(ctx, t, store, "wisp child create", waiter, true)
		})
	}
}

// seedWispParentOfDurableChild creates wisp w, its durable child d and the
// open issue a, and commits, so a blocking edge from w to a flips d's
// is_blocked through its parent.
func seedWispParentOfDurableChild(ctx context.Context, t *testing.T, store *DoltStore, w, d, a string) {
	t.Helper()
	for _, iss := range []*types.Issue{
		{ID: w, Title: "wisp parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: d, Title: "durable child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: a, Title: "blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("seed %s: %v", iss.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: d, DependsOnID: w, Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("seed parent-child: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed wisp parent')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit seed: %v", err)
	}
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: graph tables must be clean after seed, got dirty=%v", dirty)
	}
	if headIsBlocked(ctx, t, store, d) {
		t.Fatalf("precondition: HEAD must have %s unblocked", d)
	}
}

// requireCommittedIsBlocked fails unless op left the graph tables clean and
// HEAD has id's is_blocked at want: the flip was committed, not left in the
// working set.
func requireCommittedIsBlocked(ctx context.Context, t *testing.T, store *DoltStore, op, id string, want bool) {
	t.Helper()
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("%s must leave graph tables clean (is_blocked committed), got dirty=%v", op, dirty)
	}
	if got := headIsBlocked(ctx, t, store, id); got != want {
		t.Fatalf("after %s, HEAD has %s is_blocked=%t, want %t", op, id, got, want)
	}
}
