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

// TestRecomputeIsBlockedAfterMerge_MergedHeldFlagStaysPut is AC-2: the other
// database set A's flag to its own (equally valid) view — that write merges
// in — and the post-merge recompute must keep the merged value in BOTH
// directions, touch no working-set row, and journal nothing for A. The
// merged-in 1 is the direction the bug burned: base read the absent wisp as
// a satisfied blocker and cleared it, so every merge ping-ponged the flag.
// The scoped recompute and the mark-only pass over A and A's subtree must
// agree.
func TestRecomputeIsBlockedAfterMerge_MergedHeldFlagStaysPut(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// dm-a and dm-q blocked on absent wisps (merged to 0 and to 1
	// respectively below), plus the parent-child pair for the subtree half.
	createWisp(t, ctx, store, "dm-w")
	createWisp(t, ctx, store, "dm-wq")
	createPerm(t, ctx, store, "dm-a")
	createPerm(t, ctx, store, "dm-q")
	addDependencyWithMeta(t, ctx, store, "dm-a", "dm-w", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dm-q", "dm-wq", types.DepBlocks, "")
	createWisp(t, ctx, store, "dm-wp")
	createPerm(t, ctx, store, "dm-p")
	createPerm(t, ctx, store, "dm-c")
	addDependencyWithMeta(t, ctx, store, "dm-p", "dm-wp", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dm-c", "dm-p", types.DepParentChild, "")
	dropWisp(t, ctx, store, "dm-w")
	dropWisp(t, ctx, store, "dm-wq")
	dropWisp(t, ctx, store, "dm-wp")
	// Seed dm-q at 0 so the merged commit genuinely carries its flip to 1.
	setFlag(t, ctx, store, "issues", "dm-q", false)
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed dangling edges')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit seed: %v", err)
	}
	preHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read seed HEAD: %v", err)
	}

	// The "merge": the other database (which has the wisps) recomputed both
	// rows to its own view — dm-a to 0, dm-q to 1 (its wisp is open there) —
	// and those rows merged in here.
	setFlag(t, ctx, store, "issues", "dm-a", false)
	setFlag(t, ctx, store, "issues", "dm-q", true)
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote view of held flags')"); err != nil {
		t.Fatalf("commit merge: %v", err)
	}

	// The fixture's own writes journal; the claim under test is that the
	// recompute adds nothing, so snapshot per-row counts first.
	journalCount := func(id string) int {
		t.Helper()
		var n int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events WHERE issue_id = ?", id).Scan(&n); err != nil {
			t.Fatalf("count journal rows: %v", err)
		}
		return n
	}
	journalBefore := map[string]int{}
	for _, id := range []string{"dm-a", "dm-q", "dm-p", "dm-c"} {
		journalBefore[id] = journalCount(id)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin recompute tx: %v", err)
	}
	unscope := issueops.ScopeEventsJournalTransaction(tx, true)
	if err := issueops.RecomputeIsBlockedAfterMergeInTx(ctx, tx, preHead); err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeIsBlockedAfterMergeInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit recompute tx: %v", err)
	}
	unscope()

	if flagOf(t, ctx, store, "issues", "dm-a") {
		t.Error("post-merge recompute overwrote the merged is_blocked=0 with the local view; a held row keeps the merged value")
	}
	if !flagOf(t, ctx, store, "issues", "dm-q") {
		t.Error("post-merge recompute cleared the merged is_blocked=1 by reading the absent wisp as a satisfied blocker; a held row keeps the merged value")
	}
	requireCleanTables(ctx, t, store, "issues", "dependencies")
	for _, id := range []string{"dm-a", "dm-q"} {
		if got := journalCount(id); got != journalBefore[id] {
			t.Errorf("recompute journaled %d new update event(s) for %s; a row left as found journals nothing", got-journalBefore[id], id)
		}
	}

	// Scoped paths over A and over the held subtree must agree: give the
	// pair the other database's view too (dm-p 0 — its wisp is closed there,
	// dm-c 1 — it inherits the block), merged and committed the same way,
	// and every path must keep it.
	setFlag(t, ctx, store, "issues", "dm-p", false)
	setFlag(t, ctx, store, "issues", "dm-c", true)
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote view of the pair')"); err != nil {
		t.Fatalf("commit pair merge: %v", err)
	}
	scopedRecompute(t, ctx, store, []string{"dm-a", "dm-q"})
	scopedMark(t, ctx, store, []string{"dm-a", "dm-q"})
	scopedRecompute(t, ctx, store, []string{"dm-p", "dm-c"})
	scopedMark(t, ctx, store, []string{"dm-p", "dm-c"})
	requireFlags(t, ctx, store, "issues", map[string]bool{"dm-a": false, "dm-q": true, "dm-p": false, "dm-c": true})
	requireCleanTables(ctx, t, store, "issues", "dependencies")
	for _, id := range []string{"dm-p", "dm-c"} {
		if got := journalCount(id); got != journalBefore[id] {
			t.Errorf("scoped recompute journaled %d new update event(s) for held %s; a row left as found journals nothing", got-journalBefore[id], id)
		}
	}
}

// TestRecomputeAllIsBlocked_HeldRowWithVisibleReasonIsDecided is AC-3: held
// is not frozen — visible local evidence decides the row. An open blocker
// issue, a present open wisp, or a present spawner whose gate reads blocked
// each mark a held row at 0 up to 1 on every path (full repair, scoped
// recompute, mark-only), while a closed or pinned holder of a dangling edge
// is decided 0.
func TestRecomputeAllIsBlocked_HeldRowWithVisibleReasonIsDecided(t *testing.T) {
	t.Run("open blocker issue marks held row", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		createWisp(t, ctx, store, "dv-w")
		createPerm(t, ctx, store, "dv-b")
		createPerm(t, ctx, store, "dv-a")
		addDependencyWithMeta(t, ctx, store, "dv-a", "dv-w", types.DepBlocks, "")
		addDependencyWithMeta(t, ctx, store, "dv-a", "dv-b", types.DepBlocks, "")
		dropWisp(t, ctx, store, "dv-w")
		setFlag(t, ctx, store, "issues", "dv-a", false)

		fullRepair(t, ctx, store)
		requireFlags(t, ctx, store, "issues", map[string]bool{"dv-a": true})

		// Scoped recompute and mark-only over the same row agree.
		setFlag(t, ctx, store, "issues", "dv-a", false)
		scopedRecompute(t, ctx, store, []string{"dv-a"})
		if !flagOf(t, ctx, store, "issues", "dv-a") {
			t.Error("scoped recompute left a held row at 0 though an open local blocker decides it")
		}
		setFlag(t, ctx, store, "issues", "dv-a", false)
		scopedMark(t, ctx, store, []string{"dv-a"})
		if !flagOf(t, ctx, store, "issues", "dv-a") {
			t.Error("mark-only pass left a held row at 0 though an open local blocker decides it")
		}
	})

	t.Run("present open wisp marks held row", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		createWisp(t, ctx, store, "dv-gone")
		createWisp(t, ctx, store, "dv-here")
		createPerm(t, ctx, store, "dv-a2")
		addDependencyWithMeta(t, ctx, store, "dv-a2", "dv-gone", types.DepBlocks, "")
		addDependencyWithMeta(t, ctx, store, "dv-a2", "dv-here", types.DepBlocks, "")
		dropWisp(t, ctx, store, "dv-gone")
		setFlag(t, ctx, store, "issues", "dv-a2", false)

		fullRepair(t, ctx, store)
		requireFlags(t, ctx, store, "issues", map[string]bool{"dv-a2": true})
	})

	t.Run("present spawner with blocking gate marks held row", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		createWisp(t, ctx, store, "dv-gw")
		createPerm(t, ctx, store, "dv-s")
		createPerm(t, ctx, store, "dv-sk")
		createPerm(t, ctx, store, "dv-a3")
		addDependencyWithMeta(t, ctx, store, "dv-a3", "dv-gw", types.DepBlocks, "")
		addDependencyWithMeta(t, ctx, store, "dv-sk", "dv-s", types.DepParentChild, "")
		addDependencyWithMeta(t, ctx, store, "dv-a3", "dv-s", types.DepWaitsFor, "")
		dropWisp(t, ctx, store, "dv-gw")
		setFlag(t, ctx, store, "issues", "dv-a3", false)

		fullRepair(t, ctx, store)
		requireFlags(t, ctx, store, "issues", map[string]bool{"dv-a3": true})
	})

	for _, status := range []types.Status{types.StatusClosed, types.StatusPinned} {
		t.Run("closed-or-pinned holder of a dangling edge is decided 0 ("+string(status)+")", func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			createWisp(t, ctx, store, "dv-w")
			createPerm(t, ctx, store, "dv-a4")
			addDependencyWithMeta(t, ctx, store, "dv-a4", "dv-w", types.DepBlocks, "")
			dropWisp(t, ctx, store, "dv-w")
			rawSetStatus(t, ctx, store, "issues", "dv-a4", status)

			fullRepair(t, ctx, store)
			requireFlags(t, ctx, store, "issues", map[string]bool{"dv-a4": false})
		})
	}
}

// TestRecomputeAllIsBlocked_PresentWispBlockerDecidedAsToday is AC-4: a
// blocker wisp that IS present behaves exactly as it always did — decided 1
// while open, decided 0 once closed — on the full repair and the scoped
// path. The hold never applies to rows whose evidence is all here.
func TestRecomputeAllIsBlocked_PresentWispBlockerDecidedAsToday(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createWisp(t, ctx, store, "dp-w")
	createPerm(t, ctx, store, "dp-a")
	addDependencyWithMeta(t, ctx, store, "dp-a", "dp-w", types.DepBlocks, "")

	// Stale 0 is corrected up to 1, full repair and scoped.
	setFlag(t, ctx, store, "issues", "dp-a", false)
	fullRepair(t, ctx, store)
	requireFlags(t, ctx, store, "issues", map[string]bool{"dp-a": true})

	setFlag(t, ctx, store, "issues", "dp-a", false)
	scopedRecompute(t, ctx, store, []string{"dp-a"})
	if !flagOf(t, ctx, store, "issues", "dp-a") {
		t.Error("scoped recompute left a present-wisp-blocked row at 0")
	}

	// Merged-in close of the wisp: decided 0, full repair and scoped.
	rawSetStatus(t, ctx, store, "wisps", "dp-w", types.StatusClosed)
	fullRepair(t, ctx, store)
	requireFlags(t, ctx, store, "issues", map[string]bool{"dp-a": false})

	setFlag(t, ctx, store, "issues", "dp-a", true)
	scopedRecompute(t, ctx, store, []string{"dp-a"})
	if flagOf(t, ctx, store, "issues", "dp-a") {
		t.Error("scoped recompute left a row blocked at 1 after its wisp blocker closed")
	}
}

// TestRecomputeAllIsBlocked_ParentHoldsDanglingEdge_ChildHeld is AC-5: an
// open parent whose own edge dangles makes its open child's blockedness
// unknown (the parent's is_blocked is itself as-found), so the child is held
// too — left as found at 1 and at 0, and a stored 1 on the parent marks
// nothing on the child. The exceptions: a visible reason on the parent
// outside its own subtree darkens the child, and a closed or pinned child is
// decided 0.
func TestRecomputeAllIsBlocked_ParentHoldsDanglingEdge_ChildHeld(t *testing.T) {
	t.Run("child left as found at 1 and at 0", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		createWisp(t, ctx, store, "dpc-w")
		createPerm(t, ctx, store, "dpc-p")
		addDependencyWithMeta(t, ctx, store, "dpc-p", "dpc-w", types.DepBlocks, "")
		createPerm(t, ctx, store, "dpc-c")
		addDependencyWithMeta(t, ctx, store, "dpc-c", "dpc-p", types.DepParentChild, "")
		dropWisp(t, ctx, store, "dpc-w")
		// The child inherited 1 from the blocked parent before the drop; pin
		// the parent at 1 and try the child at both values.
		require := func(childBlocked bool) {
			t.Helper()
			setFlag(t, ctx, store, "issues", "dpc-p", true)
			setFlag(t, ctx, store, "issues", "dpc-c", childBlocked)
			fullRepair(t, ctx, store)
			requireFlags(t, ctx, store, "issues", map[string]bool{"dpc-p": true, "dpc-c": childBlocked})
		}
		require(true)
		require(false)
	})

	t.Run("parent's visible reason outside its subtree darkens the child", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		createWisp(t, ctx, store, "dpe-w")
		createPerm(t, ctx, store, "dpe-x")
		createPerm(t, ctx, store, "dpe-p")
		addDependencyWithMeta(t, ctx, store, "dpe-p", "dpe-w", types.DepBlocks, "")
		addDependencyWithMeta(t, ctx, store, "dpe-p", "dpe-x", types.DepBlocks, "")
		createPerm(t, ctx, store, "dpe-c")
		addDependencyWithMeta(t, ctx, store, "dpe-c", "dpe-p", types.DepParentChild, "")
		dropWisp(t, ctx, store, "dpe-w")
		setFlag(t, ctx, store, "issues", "dpe-c", false)

		fullRepair(t, ctx, store)
		requireFlags(t, ctx, store, "issues", map[string]bool{"dpe-p": true, "dpe-c": true})
	})

	t.Run("epic whose close gate targets an absent wisp child holds its own children", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		// The epic waits-for its own wisp child; the child is not in this
		// database, so the gate — and with it the epic — is held.
		createWisp(t, ctx, store, "dpg-wc")
		createPerm(t, ctx, store, "dpg-p")
		addDependencyWithMeta(t, ctx, store, "dpg-p", "dpg-wc", types.DepWaitsFor, "")
		addDependencyWithMeta(t, ctx, store, "dpg-wc", "dpg-p", types.DepParentChild, "")
		createPerm(t, ctx, store, "dpg-c")
		addDependencyWithMeta(t, ctx, store, "dpg-c", "dpg-p", types.DepParentChild, "")
		dropWisp(t, ctx, store, "dpg-wc")

		setFlag(t, ctx, store, "issues", "dpg-p", true)
		setFlag(t, ctx, store, "issues", "dpg-c", false)
		fullRepair(t, ctx, store)
		requireFlags(t, ctx, store, "issues", map[string]bool{"dpg-p": true, "dpg-c": false})
	})

	for _, status := range []types.Status{types.StatusClosed, types.StatusPinned} {
		t.Run("closed-or-pinned child is decided 0 ("+string(status)+")", func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			createWisp(t, ctx, store, "dpz-w")
			createPerm(t, ctx, store, "dpz-p")
			addDependencyWithMeta(t, ctx, store, "dpz-p", "dpz-w", types.DepBlocks, "")
			createPerm(t, ctx, store, "dpz-c")
			addDependencyWithMeta(t, ctx, store, "dpz-c", "dpz-p", types.DepParentChild, "")
			dropWisp(t, ctx, store, "dpz-w")
			setFlag(t, ctx, store, "issues", "dpz-p", true)
			setFlag(t, ctx, store, "issues", "dpz-c", true)
			rawSetStatus(t, ctx, store, "issues", "dpz-c", status)

			fullRepair(t, ctx, store)
			requireFlags(t, ctx, store, "issues", map[string]bool{"dpz-p": true, "dpz-c": false})
		})
	}
}

// TestCountIsBlockedHeld_AfterRepair is AC-6's database half: on a repaired
// database with dangling edges the stale count is 0 and the held count names
// exactly the rows left as found; on a database where every wisp named by a
// dependency row is present, the held count is 0 (the probe answers first).
// The message render for both is pinned in cmd/bd/doctor/blocked_test.go.
func TestCountIsBlockedHeld_AfterRepair(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Nothing dangling yet: the held count reads 0 before any fixture.
	held, err := issueops.CountIsBlockedHeldInTx(ctx, store.db)
	if err != nil {
		t.Fatalf("clean held count: %v", err)
	}
	if held != 0 {
		t.Errorf("clean database held count = %d, want 0", held)
	}

	// Two direct holders plus one held child.
	createWisp(t, ctx, store, "dc-w1")
	createWisp(t, ctx, store, "dc-w2")
	createPerm(t, ctx, store, "dc-a1")
	createPerm(t, ctx, store, "dc-a2")
	createPerm(t, ctx, store, "dc-c1")
	addDependencyWithMeta(t, ctx, store, "dc-a1", "dc-w1", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dc-a2", "dc-w2", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dc-c1", "dc-a1", types.DepParentChild, "")
	dropWisp(t, ctx, store, "dc-w1")
	dropWisp(t, ctx, store, "dc-w2")

	fullRepair(t, ctx, store)
	if stale, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, store.db); err != nil || stale != 0 {
		t.Fatalf("repaired stale count: want 0/nil, got %d/%v", stale, err)
	}
	held, err = issueops.CountIsBlockedHeldInTx(ctx, store.db)
	if err != nil {
		t.Fatalf("repaired held count: %v", err)
	}
	if held != 3 {
		t.Errorf("repaired held count = %d, want 3 (two direct holders and one held child)", held)
	}
}
