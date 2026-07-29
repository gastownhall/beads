package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// countInconsistencies wraps the read-only detection used by the bd doctor
// Blocked State check.
func countInconsistencies(ctx context.Context, t *testing.T, db *sql.DB) int64 {
	t.Helper()
	n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, db)
	if err != nil {
		t.Fatalf("CountIsBlockedInconsistenciesInTx: %v", err)
	}
	return n
}

// recomputeAll wraps the full repair used by 'bd doctor --fix' and returns the
// number of rows it corrected.
func recomputeAll(ctx context.Context, t *testing.T, db *sql.DB) int64 {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin recompute-all tx: %v", err)
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeAllIsBlockedInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit recompute-all tx: %v", err)
	}
	return changed
}

// TestRecomputeAllIsBlocked_RepairsStaleClearedFlag is the bd-6dnrw.37 repair
// path: a row that SHOULD be blocked but whose is_blocked was left at 0 (the
// shape a skipped post-pull recompute leaves behind). Detection must see it,
// the full recompute must fix it, and detection and repair must then agree the
// database is consistent — the lockstep that keeps the COUNT predicate from
// drifting from the recompute SQL.
func TestRecomputeAllIsBlocked_RepairsStaleClearedFlag(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Correct graph via the normal write path: bm-w blocked on open bm-x.
	seedBlockedPair(ctx, t, store, true)
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("precondition: bm-w should be blocked by open bm-x")
	}
	// A correctly-maintained graph has zero inconsistencies.
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("consistent graph: want 0 inconsistencies, got %d", n)
	}

	// Corrupt: clear bm-w's flag directly, with no recompute — exactly what a
	// merge that bypassed the recompute hook leaves behind.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("corrupt is_blocked: %v", err)
	}
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Fatalf("after corruption: want 1 inconsistency, got %d", n)
	}

	// Repair via the full recompute (the always-available path that does not
	// need a pull to advance HEAD).
	if changed := recomputeAll(ctx, t, store.db); changed != 1 {
		t.Fatalf("repair: want 1 row corrected, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("after repair: bm-w must read blocked again")
	}
	// Detection and repair now agree, and the repair is idempotent.
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("after repair: want 0 inconsistencies, got %d", n)
	}
	if again := recomputeAll(ctx, t, store.db); again != 0 {
		t.Fatalf("repair must be idempotent: want 0 on second run, got %d", again)
	}
}

// TestRecomputeAllIsBlocked_ClearsStuckBlockedFlag is the mirror case: a row
// left is_blocked = 1 after its only blocker was closed remotely (a merge that
// bypassed the recompute hook). `bd ready` would keep hiding it; the full
// recompute must clear the flag.
func TestRecomputeAllIsBlocked_ClearsStuckBlockedFlag(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("precondition: bm-w should be blocked by open bm-x")
	}

	// "Merge": the remote closed the blocker; no local recompute ran, so bm-w
	// is stuck is_blocked = 1 with a closed blocker.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("simulate merged close: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("setup: bm-w must still read blocked before the recompute (the stale flag is the bug)")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Fatalf("after merged close: want 1 inconsistency, got %d", n)
	}

	if changed := recomputeAll(ctx, t, store.db); changed != 1 {
		t.Fatalf("repair: want 1 row corrected, got %d", changed)
	}
	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("after repair: bm-w must be unblocked (its only blocker is closed)")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("after repair: want 0 inconsistencies, got %d", n)
	}
}

// TestRecomputeAllIsBlocked_CascadesThroughParentChild verifies the fixpoint:
// is_blocked propagates from a blocked parent to its child across passes. The
// single-pass detection COUNT is a documented lower bound here — it sees only
// the parent on the first pass — but the recompute corrects the whole chain and
// detection reaches 0 once it converges.
func TestRecomputeAllIsBlocked_CascadesThroughParentChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// bm-w blocked on open bm-x; bm-y is a child of bm-w, so bm-y inherits
	// blocked. All maintained by the normal write path.
	seedBlockedPair(ctx, t, store, true)
	child := &types.Issue{ID: "bm-y", Title: "bm-y", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bm-y", DependsOnID: "bm-w", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed parent-child chain')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit chain: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-y") {
		t.Fatal("precondition: child bm-y should inherit blocked from parent bm-w")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("consistent chain: want 0 inconsistencies, got %d", n)
	}

	// Corrupt both the parent and the child to is_blocked = 0.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id IN ('bm-w', 'bm-y')"); err != nil {
		t.Fatalf("corrupt chain: %v", err)
	}
	// Single-pass detection sees only the parent (the child's parent-child
	// reason depends on the parent's still-corrupted flag) — a lower bound.
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Fatalf("after corruption: want 1 (single-pass lower bound), got %d", n)
	}

	// The fixpoint corrects the whole chain across passes.
	if changed := recomputeAll(ctx, t, store.db); changed != 2 {
		t.Fatalf("repair: want 2 rows corrected across passes, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") || !isBlocked(ctx, t, store.db, "bm-y") {
		t.Fatal("after repair: both bm-w and bm-y must read blocked")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("after repair: want 0 inconsistencies, got %d", n)
	}
}
