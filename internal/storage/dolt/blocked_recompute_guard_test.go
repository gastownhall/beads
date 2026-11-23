package dolt

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// workingSetGraphDirty returns the committable graph tables (issues,
// dependencies) the guard reports as dirty.
func workingSetGraphDirty(ctx context.Context, t *testing.T, store *DoltStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx,
		"SELECT DISTINCT table_name FROM dolt_status WHERE table_name IN ('issues','dependencies') ORDER BY table_name")
	if err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	defer rows.Close()
	var dirty []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		dirty = append(dirty, name)
	}
	return dirty
}

// TestRecomputeAllBlocked_RefusesDirtyIssues is the bd-6dnrw.37 release-safety
// guard: a full recompute stages the whole `issues` table and derives flags
// from the current graph, so it must refuse to run while `issues` has unrelated
// uncommitted edits rather than sweep them into the repair commit.
func TestRecomputeAllBlocked_RefusesDirtyIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true) // clean, consistent committed tree

	// Unrelated dirty edit the operator has not committed.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'uncommitted edit' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("dirty issues: %v", err)
	}

	changed, err := store.RecomputeAllBlocked(ctx)
	if !errors.Is(err, issueops.ErrBlockedRecomputeDirtyGraph) {
		t.Fatalf("want ErrBlockedRecomputeDirtyGraph, got changed=%d err=%v", changed, err)
	}
	if changed != 0 {
		t.Fatalf("refused recompute must report 0 rows, got %d", changed)
	}
	// The edit must remain uncommitted in the working set, not swept into a commit.
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 1 || dirty[0] != "issues" {
		t.Fatalf("issues edit must still be dirty (unswept), got dirty=%v", dirty)
	}
}

// TestRecomputeAllBlocked_RefusesDirtyDependencies guards the other half of the
// finding: is_blocked is derived from `dependencies`, so committing flags while
// dependency edits are uncommitted would publish state derived from a graph that
// is not part of the same commit.
func TestRecomputeAllBlocked_RefusesDirtyDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)

	// Uncommitted dependency-graph edit.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM dependencies WHERE issue_id = 'bm-w'"); err != nil {
		t.Fatalf("dirty dependencies: %v", err)
	}

	changed, err := store.RecomputeAllBlocked(ctx)
	if !errors.Is(err, issueops.ErrBlockedRecomputeDirtyGraph) {
		t.Fatalf("want ErrBlockedRecomputeDirtyGraph, got changed=%d err=%v", changed, err)
	}
	if changed != 0 {
		t.Fatalf("refused recompute must report 0 rows, got %d", changed)
	}
}

// TestRecomputeAllBlocked_AllowsDirtyWisps pins the deliberate exclusion of the
// dolt-ignored wisp tables from the guard. Dolt reports every ignored table as
// perpetually modified, so guarding on wisps would refuse the recompute in any
// workspace that uses them. A stale committed flag must still be repaired even
// while the wisp tables are dirty.
func TestRecomputeAllBlocked_AllowsDirtyWisps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)

	// A throwaway wisp leaves the dolt-ignored wisp tables perpetually dirty.
	scratch := &types.Issue{ID: "bm-wisp", Title: "scratch", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, scratch, "tester"); err != nil {
		t.Fatalf("create scratch issue: %v", err)
	}
	if err := store.UpdateIssue(ctx, "bm-wisp", map[string]interface{}{"no_history": true}, "tester"); err != nil {
		t.Fatalf("demote scratch to wisp: %v", err)
	}

	// Commit a stale is_blocked (the shape a skipped post-pull recompute leaves),
	// so the only remaining working-set dirt is the ignored wisp tables.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("corrupt is_blocked: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit stale flag + scratch demotion')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit stale flag: %v", err)
	}
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: committable graph must be clean, got dirty=%v", dirty)
	}

	// The recompute must run (not refuse) despite perpetually-dirty wisps.
	changed, err := store.RecomputeAllBlocked(ctx)
	if err != nil {
		t.Fatalf("recompute must run with only wisps dirty: %v", err)
	}
	if changed != 1 {
		t.Fatalf("want 1 row repaired, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("bm-w must read blocked again after repair")
	}
}

// TestRecomputeAllBlocked_RepairsAndCommitsWhenClean covers the changed>0 commit
// branch of the store method: on a clean tree a stale flag is repaired and the
// correction is committed (the working set returns to clean), and the repair is
// idempotent.
func TestRecomputeAllBlocked_RepairsAndCommitsWhenClean(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)

	// Commit a stale flag so the tree is clean going into the recompute.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("corrupt is_blocked: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit stale flag')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit stale flag: %v", err)
	}

	changed, err := store.RecomputeAllBlocked(ctx)
	if err != nil {
		t.Fatalf("recompute on clean tree: %v", err)
	}
	if changed != 1 {
		t.Fatalf("want 1 row repaired, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("bm-w must read blocked again after repair")
	}
	// The repair was committed: the committable graph is clean again.
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("repair must be committed, leaving a clean tree, got dirty=%v", dirty)
	}
	// Idempotent: a second pass corrects nothing and stays clean.
	if again, err := store.RecomputeAllBlocked(ctx); err != nil || again != 0 {
		t.Fatalf("recompute must be idempotent: got changed=%d err=%v", again, err)
	}
}

// TestRecomputeAllBlocked_ReportsRowsLeftAsFound runs the store's repair, as
// bd recompute-blocked reaches it (storage.RecomputeBlocked), over a database
// that holds one stale flag and one issue blocked on a wisp it has no row for.
// The stale flag is a correction. The other row is neither corrected nor
// confirmed, and the result says so in RowsAbstained, on the run that repairs
// and again on the run that has nothing to repair: the count describes the
// database, not the run.
func TestRecomputeAllBlocked_ReportsRowsLeftAsFound(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)
	createPerm(t, ctx, store, "ra-a")
	createWisp(t, ctx, store, "ra-w")
	addDependencyWithMeta(t, ctx, store, "ra-a", "ra-w", types.DepBlocks, "")
	if !isBlocked(ctx, t, store.db, "ra-a") {
		t.Fatal("precondition: ra-a should be blocked by open wisp ra-w")
	}
	commitGraph := func(msg string) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", msg); err != nil && !isDoltNothingToCommit(err) {
			t.Fatalf("%s: %v", msg, err)
		}
	}
	commitGraph("seed an issue blocked on a wisp")

	// With the wisp here, every row is decided.
	got, err := storage.RecomputeBlocked(ctx, store)
	if err != nil || got != (storage.RecomputeBlockedResult{}) {
		t.Fatalf("wisp present: RecomputeBlocked = (%+v, %v), want the zero result", got, err)
	}

	// The clone's state, plus one stale flag for the repair to correct.
	loseWisp(ctx, t, store, "ra-w")
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("corrupt is_blocked: %v", err)
	}
	commitGraph("commit stale flag")

	got, err = storage.RecomputeBlocked(ctx, store)
	if err != nil {
		t.Fatalf("RecomputeBlocked: %v", err)
	}
	if want := (storage.RecomputeBlockedResult{RowsCorrected: 1, RowsAbstained: 1}); got != want {
		t.Fatalf("RecomputeBlocked = %+v, want %+v", got, want)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("bm-w must read blocked again after repair")
	}
	if !isBlocked(ctx, t, store.db, "ra-a") {
		t.Error("ra-a was cleared: the repair decided a row it reported as left alone")
	}

	got, err = storage.RecomputeBlocked(ctx, store)
	if err != nil {
		t.Fatalf("second RecomputeBlocked: %v", err)
	}
	if want := (storage.RecomputeBlockedResult{RowsAbstained: 1}); got != want {
		t.Fatalf("second RecomputeBlocked = %+v, want %+v", got, want)
	}
	// The older entry point reports the same repair without the count.
	if changed, err := store.RecomputeAllBlocked(ctx); err != nil || changed != 0 {
		t.Fatalf("RecomputeAllBlocked = (%d, %v), want (0, nil)", changed, err)
	}
}

// TestRecomputeAllBlocked_CountsRowsLeftAsFoundAfterTheRepair pins when the
// count is read. A child holds an edge to a wisp this database does not have,
// and its parent is blocked by an open issue but carries a stale 0. Before the
// repair the child is undecided: nothing visible blocks it. The repair marks
// the parent, the parent's flag then decides the child, and the result must
// say two rows corrected and none left as found. A count read before the
// repair would report the child as left alone in the same result that counts
// it as corrected.
func TestRecomputeAllBlocked_CountsRowsLeftAsFoundAfterTheRepair(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "rc-x")
	createPerm(t, ctx, store, "rc-p")
	createPerm(t, ctx, store, "rc-c")
	createWisp(t, ctx, store, "rc-w")
	addDependencyWithMeta(t, ctx, store, "rc-p", "rc-x", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "rc-c", "rc-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "rc-c", "rc-w", types.DepBlocks, "")
	loseWisp(ctx, t, store, "rc-w")
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id IN ('rc-p', 'rc-c')"); err != nil {
		t.Fatalf("plant stale flags: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit stale parent and child')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit stale flags: %v", err)
	}
	if n := countUndecided(ctx, t, store.db); n != 1 {
		t.Fatalf("precondition: %d undecided rows before the repair, want 1 (rc-c under an unmarked parent)", n)
	}

	got, err := storage.RecomputeBlocked(ctx, store)
	if err != nil {
		t.Fatalf("RecomputeBlocked: %v", err)
	}
	if want := (storage.RecomputeBlockedResult{RowsCorrected: 2}); got != want {
		t.Fatalf("RecomputeBlocked = %+v, want %+v", got, want)
	}
	if !isBlocked(ctx, t, store.db, "rc-p") || !isBlocked(ctx, t, store.db, "rc-c") {
		t.Errorf("after the repair: rc-p blocked %v, rc-c blocked %v; want both",
			isBlocked(ctx, t, store.db, "rc-p"), isBlocked(ctx, t, store.db, "rc-c"))
	}
}

// TestRecomputeAllBlocked_StaleParentDoesNotDarkenAnUndecidedChild is the
// other direction (the review of gastownhall/beads#8597): the parent has no
// blocker at all but carries a stale 1, and the child, which holds an edge to
// a wisp this database does not have, carries the merged 0. The parent's
// stale flag must not leave a mark on the child on its way out: a fixpoint
// that marked the child from it and cleared the parent afterwards could never
// clear the child again, because nothing this database sees decides it. The
// repair corrects the parent alone and leaves the child as found, and the
// scoped recompute a write runs does the same.
func TestRecomputeAllBlocked_StaleParentDoesNotDarkenAnUndecidedChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "rs-p")
	createPerm(t, ctx, store, "rs-c")
	createWisp(t, ctx, store, "rs-w")
	addDependencyWithMeta(t, ctx, store, "rs-c", "rs-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "rs-c", "rs-w", types.DepBlocks, "")
	loseWisp(ctx, t, store, "rs-w")
	plantStale := func() {
		t.Helper()
		plantIsBlocked(ctx, t, store, "rs-p", true)
		plantIsBlocked(ctx, t, store, "rs-c", false)
		if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit stale parent')"); err != nil && !isDoltNothingToCommit(err) {
			t.Fatalf("commit stale flags: %v", err)
		}
	}
	plantStale()
	// The doctor's single pass reads rs-p's stale 1 and so counts rs-c too;
	// the repair clears rs-p first and leaves rs-c alone (countStaleIsBlockedSQL).
	if n := countInconsistencies(ctx, t, store.db); n != 2 {
		t.Errorf("precondition: doctor counts %d stale rows, want 2 (rs-p, and rs-c through it)", n)
	}

	got, err := storage.RecomputeBlocked(ctx, store)
	if err != nil {
		t.Fatalf("RecomputeBlocked: %v", err)
	}
	if want := (storage.RecomputeBlockedResult{RowsCorrected: 1, RowsAbstained: 1}); got != want {
		t.Errorf("RecomputeBlocked = %+v, want %+v", got, want)
	}
	want := map[string]bool{"rs-p": false, "rs-c": false}
	assertBlockedFlags(ctx, t, store, "after the full repair", want)
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("after the full repair: doctor counts %d stale rows, want 0", n)
	}

	plantStale()
	recomputeScoped(ctx, t, store.db, "rs-p", "rs-c")
	assertBlockedFlags(ctx, t, store, "after the scoped recompute", want)
}
