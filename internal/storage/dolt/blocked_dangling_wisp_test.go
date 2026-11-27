package dolt

// bd-muhnf0: on a clone, `dependencies` is versioned while `wisps` is
// dolt_ignored, and dependencies carries no FK onto wisps — so an edge can
// name a wisp this database does not have. Every should-be-blocked union leg
// that reaches a wisp inner-JOINS wisps, so the absent wisp reads as a
// satisfied blocker and the repair clears the dependent's flag into the
// versioned issues table, where the next merge from the database that DOES
// have the wisp flips it back — merge ping-pong.
//
// The contract these tests pin: a row whose blockedness depends on an absent
// wisp is HELD — left exactly as found by every repair path — unless visible
// local evidence (a present open blocker, a present spawner whose gate reads
// blocked) decides it, in which case that evidence decides it.

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// dropWisp simulates the clone: the wisp row is not in this database, the
// dependency rows that name it are (wisps is dolt_ignored, dependencies is
// versioned and has no FK onto wisps).
func dropWisp(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, "DELETE FROM wisps WHERE id = ?", id); err != nil {
		t.Fatalf("drop wisp %s: %v", id, err)
	}
}

// setFlag simulates a merged-in write (exactly what a merge does: the raw
// column value arrives with no local write path), for fixture shaping only.
func setFlag(t *testing.T, ctx context.Context, store *DoltStore, table, id string, blocked bool) {
	t.Helper()
	v := 0
	if blocked {
		v = 1
	}
	//nolint:gosec // G201: table is a hardcoded "issues" or "wisps" from callers.
	if _, err := store.db.ExecContext(ctx, "UPDATE "+table+" SET is_blocked = ? WHERE id = ?", v, id); err != nil {
		t.Fatalf("set %s.%s flag: %v", table, id, err)
	}
}

// rawSetStatus changes a row's status without the write path, the way a
// merged-in close or pin arrives.
func rawSetStatus(t *testing.T, ctx context.Context, store *DoltStore, table, id string, status types.Status) {
	t.Helper()
	//nolint:gosec // G201: table is a hardcoded "issues" or "wisps" from callers.
	if _, err := store.db.ExecContext(ctx, "UPDATE "+table+" SET status = ? WHERE id = ?", string(status), id); err != nil {
		t.Fatalf("set %s.%s status: %v", table, id, err)
	}
}

func flagOf(t *testing.T, ctx context.Context, store *DoltStore, table, id string) bool {
	t.Helper()
	flags := readIsBlockedFlags(t, ctx, store, table)
	got, ok := flags[id]
	if !ok {
		t.Fatalf("row %s not found in %s", id, table)
	}
	return got
}

// fullRepair runs RecomputeAllIsBlockedInTx in its own transaction.
func fullRepair(t *testing.T, ctx context.Context, store *DoltStore) int64 {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeAllIsBlockedInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return changed
}

// scopedRecompute runs RecomputeIsBlockedInTx over exactly ids.
func scopedRecompute(t *testing.T, ctx context.Context, store *DoltStore, ids []string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := issueops.RecomputeIsBlockedInTx(ctx, tx, ids, nil, "tester"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeIsBlockedInTx(%v): %v", ids, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// scopedMark runs MarkIsBlockedInTx over exactly ids.
func scopedMark(t *testing.T, ctx context.Context, store *DoltStore, ids []string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := issueops.MarkIsBlockedInTx(ctx, tx, ids, nil, "tester"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("MarkIsBlockedInTx(%v): %v", ids, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// requireHeld asserts every named flag is exactly as the caller says and that
// detection agrees the database is repaired (0 stale).
func requireFlags(t *testing.T, ctx context.Context, store *DoltStore, table string, want map[string]bool) {
	t.Helper()
	for id, blocked := range want {
		if got := flagOf(t, ctx, store, table, id); got != blocked {
			t.Errorf("%s.%s: is_blocked=%v, want left as found at %v", table, id, got, blocked)
		}
	}
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, store.db); err != nil || n != 0 {
		t.Errorf("stale count after repair: want 0/nil, got %d/%v", n, err)
	}
}

// TestRecomputeAllIsBlocked_DanglingEdgesOfEveryKindAreHeld is AC-1's edge
// matrix: every dependency kind that can name a wisp — blocks,
// conditional-blocks, parent-child, and waits-for onto a spawner with a
// visible open durable child — leaves the dependent's flag exactly as found
// when the named wisp is absent, instead of reading the absent wisp as a
// satisfied blocker. The waits-for row is the sharpest case: the spawner's
// open child is visible here, so the gate LOOKS decided, but the spawner's
// own status (and any wisp child of it, invisible here) decides the
// any-children carve-out, so the row is still held.
func TestRecomputeAllIsBlocked_DanglingEdgesOfEveryKindAreHeld(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// blocks and conditional-blocks onto an absent wisp.
	createWisp(t, ctx, store, "dd-w1")
	createWisp(t, ctx, store, "dd-w2")
	createPerm(t, ctx, store, "dd-a1")
	createPerm(t, ctx, store, "dd-a2")
	addDependencyWithMeta(t, ctx, store, "dd-a1", "dd-w1", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dd-a2", "dd-w2", types.DepConditionalBlocks, "")

	// parent-child onto an absent wisp parent.
	createWisp(t, ctx, store, "dd-wp")
	createPerm(t, ctx, store, "dd-a3")
	addDependencyWithMeta(t, ctx, store, "dd-a3", "dd-wp", types.DepParentChild, "")

	// waits-for an absent wisp spawner whose gate a visible open durable
	// child keeps reading blocked (any-children, so the carve-out is live).
	createWisp(t, ctx, store, "dd-ws")
	createPerm(t, ctx, store, "dd-sc")
	createPerm(t, ctx, store, "dd-a4")
	addDependencyWithMeta(t, ctx, store, "dd-sc", "dd-ws", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dd-a4", "dd-ws", types.DepWaitsFor, `{"gate":"any-children"}`)

	flags := readIsBlockedFlags(t, ctx, store, "issues")
	for _, id := range []string{"dd-a1", "dd-a2", "dd-a4"} {
		if !flags[id] {
			t.Fatalf("fixture: %s should be blocked while its wisp target is present", id)
		}
	}
	if flags["dd-a3"] {
		t.Fatal("fixture: dd-a3 should be unblocked while its wisp parent is present and unblocked")
	}

	// The clone: wisps gone, edges remain.
	for _, id := range []string{"dd-w1", "dd-w2", "dd-wp", "dd-ws"} {
		dropWisp(t, ctx, store, id)
	}

	// Flag=1 rows keep 1 …
	fullRepair(t, ctx, store)
	requireFlags(t, ctx, store, "issues", map[string]bool{
		"dd-a1": true, "dd-a2": true, "dd-a4": true, // as found
		"dd-a3": false, // as found (0)
		"dd-sc": false, // spawner gone; the child is held at its found 0 too
	})

	// … and after inverting every one of them, the same repair leaves each at
	// the value it found (0 stays 0), so the repair is not idempotent-clearing.
	setFlag(t, ctx, store, "issues", "dd-a1", false)
	setFlag(t, ctx, store, "issues", "dd-a2", false)
	setFlag(t, ctx, store, "issues", "dd-a4", false)
	setFlag(t, ctx, store, "issues", "dd-a3", true)
	fullRepair(t, ctx, store)
	requireFlags(t, ctx, store, "issues", map[string]bool{
		"dd-a1": false, "dd-a2": false, "dd-a4": false, // as found at 0
		"dd-a3": true, // as found at 1
	})
}

// TestRecomputeAllBlockedOnConn_DanglingEdgeNoCorrectionNoCommit is AC-1's
// `bd recompute-blocked` entry point: the guarded conn repair corrects 0
// rows, keeps the flag as found, and — precisely because nothing changed —
// lands no "bd: recompute is_blocked (full)" commit.
func TestRecomputeAllBlockedOnConn_DanglingEdgeNoCorrectionNoCommit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createWisp(t, ctx, store, "ddc-w")
	createPerm(t, ctx, store, "ddc-a")
	addDependencyWithMeta(t, ctx, store, "ddc-a", "ddc-w", types.DepBlocks, "")
	dropWisp(t, ctx, store, "ddc-w")

	// The conn repair guards on a clean issues/dependencies working set.
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed dangling edge')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit seed: %v", err)
	}
	before := doltCommitCount(ctx, t, store)

	changed, err := versioncontrolops.RecomputeAllBlockedOnConn(ctx, store.db, "tester")
	if err != nil {
		t.Fatalf("RecomputeAllBlockedOnConn: %v", err)
	}
	if changed != 0 {
		t.Errorf("conn repair corrected %d rows over a database whose only defect is a dangling wisp edge; want 0 (flag left as found)", changed)
	}
	if !flagOf(t, ctx, store, "issues", "ddc-a") {
		t.Error("conn repair cleared is_blocked on a row whose blocker wisp is absent; want left as found")
	}
	if doltHasCommitMessage(ctx, t, store, versioncontrolops.BlockedRecomputeCommitMsg) {
		t.Error("conn repair committed a recompute that corrected nothing")
	}
	if after := doltCommitCount(ctx, t, store); after != before {
		t.Errorf("commit count moved %d -> %d over a zero-correction repair", before, after)
	}
}
