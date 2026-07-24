package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// seedBlockedPair creates issues W and X with W blocked on X through the
// normal write path (which maintains is_blocked), commits, and returns the
// commit hash — the "pre-pull HEAD" for the merge-simulation tests.
func seedBlockedPair(ctx context.Context, t *testing.T, store *DoltStore, withEdge bool) string {
	t.Helper()
	for _, id := range []string{"bm-w", "bm-x"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	if withEdge {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bm-w", DependsOnID: "bm-x", Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("seed dependency: %v", err)
		}
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed blocked pair')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit seed: %v", err)
	}
	hash, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read seed HEAD: %v", err)
	}
	return hash
}

func isBlocked(ctx context.Context, t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var blocked bool
	if err := db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&blocked); err != nil {
		t.Fatalf("read is_blocked of %s: %v", id, err)
	}
	return blocked
}

func recomputeAfterMerge(ctx context.Context, t *testing.T, db *sql.DB, fromCommit string) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin recompute tx: %v", err)
	}
	if err := issueops.RecomputeIsBlockedAfterMergeInTx(ctx, tx, fromCommit); err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeIsBlockedAfterMergeInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit recompute tx: %v", err)
	}
}

// TestRecomputeIsBlockedAfterMerge_BlockerClosedRemotely is the bd-6dnrw.3
// audit scenario, with the remote clone's merged-in writes simulated by raw
// SQL (exactly what a merge does: rows change with no local write path
// running). The blocker X is closed "remotely"; without the post-merge
// recompute, W stays is_blocked=1 with a closed blocker and `bd ready`
// silently hides it.
func TestRecomputeIsBlockedAfterMerge_BlockerClosedRemotely(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	preHead := seedBlockedPair(ctx, t, store, true)
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("precondition: bm-w should be blocked by open bm-x")
	}

	// "Merge": the remote closed the blocker; no local recompute ran.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("simulate merged close: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote closed bm-x')"); err != nil {
		t.Fatalf("commit merged close: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("setup: bm-w must still read blocked before the recompute (that staleness is the bug)")
	}

	recomputeAfterMerge(ctx, t, store.db, preHead)

	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("bm-w still is_blocked=1 after merge recompute though its only blocker is closed")
	}
}

// TestRecomputeIsBlockedAfterMerge_EdgeAddedRemotely covers the other
// direction: the merge brings a NEW blocking edge from the remote, so the
// local W must flip to blocked.
func TestRecomputeIsBlockedAfterMerge_EdgeAddedRemotely(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	preHead := seedBlockedPair(ctx, t, store, false)
	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("precondition: bm-w should start unblocked")
	}

	// "Merge": the remote added W blocks-on X (deterministic id, like every
	// post-#4259 insert site).
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (?, 'bm-w', 'bm-x', 'blocks', 'remote')",
		depid.New("bm-w", "bm-x")); err != nil {
		t.Fatalf("simulate merged edge: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote added edge')"); err != nil {
		t.Fatalf("commit merged edge: %v", err)
	}

	recomputeAfterMerge(ctx, t, store.db, preHead)

	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("bm-w not blocked after merge brought a blocking edge on open bm-x")
	}
}

// TestRecomputeIsBlockedAfterMerge_NoMergeNoOp: a pull that merged nothing
// (HEAD unchanged) must not touch anything.
func TestRecomputeIsBlockedAfterMerge_NoMergeNoOp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	head := seedBlockedPair(ctx, t, store, true)
	recomputeAfterMerge(ctx, t, store.db, head)

	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("no-op recompute changed bm-w")
	}
}

// TestRecomputeIsBlockedAfterMerge_UnknownFromCommitFullPass: an empty
// fromCommit (pre-pull HEAD unreadable) must degrade to a full recompute, not
// skip the hook.
func TestRecomputeIsBlockedAfterMerge_UnknownFromCommitFullPass(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("simulate merged close: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote closed bm-x')"); err != nil {
		t.Fatalf("commit merged close: %v", err)
	}

	recomputeAfterMerge(ctx, t, store.db, "")

	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("full-pass recompute left bm-w stale")
	}
}

// TestRecomputeIsBlockedAfterMerge_PendingMarkerWidensWindow covers the
// bd-578h9.11 permanent-skip hole: a recompute that fails AFTER its merge
// committed leaves the pending marker; the next recompute's fromCommit is the
// post-merge HEAD, so without the marker the head==fromCommit skip reads the
// lost window as "nothing merged" forever. The marker must widen the window
// back to the failed attempt's pre-merge HEAD and be cleared on success.
func TestRecomputeIsBlockedAfterMerge_PendingMarkerWidensWindow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	preHead := seedBlockedPair(ctx, t, store, true)

	// The merge committed (remote closed the blocker), but its recompute
	// failed — all that survives of the attempt is the pending marker.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("simulate merged close: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote closed bm-x')"); err != nil {
		t.Fatalf("commit merged close: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin marker tx: %v", err)
	}
	if err := issueops.MarkIsBlockedRecomputePendingInTx(ctx, tx, preHead); err != nil {
		_ = tx.Rollback()
		t.Fatalf("mark pending: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit marker: %v", err)
	}

	// The retry runs as the NEXT pull would: fromCommit == current HEAD,
	// nothing newly merged. Without the marker this is exactly the
	// NoMergeNoOp skip.
	head, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	recomputeAfterMerge(ctx, t, store.db, head)

	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("bm-w still is_blocked=1: pending marker did not widen the recompute window")
	}
	var markers int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_recompute_pending'").Scan(&markers); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if markers != 0 {
		t.Error("pending marker survived a successful recompute")
	}
}

// TestMergeRecomputesIsBlocked covers the bd-578h9.11 coverage gap: merges
// that land via store.Merge (bd vc merge, Sync's merge step) bring in writes
// that bypassed every local is_blocked hook, exactly like a pull's merge —
// but only the pull paths ran the recompute.
func TestMergeRecomputesIsBlocked(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// setupTestStore isolates tests by branch on a shared database; stay on
	// this test's branch (checking out 'main' would pollute the shared base).
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	seedBlockedPair(ctx, t, store, true)
	if !isBlocked(ctx, t, db, "bm-w") {
		t.Fatal("precondition: bm-w should be blocked by open bm-x")
	}

	// Peer branch closes the blocker with raw SQL (a merged-in write: no
	// local write path, no recompute hook).
	for _, q := range []string{
		"CALL DOLT_BRANCH('bmpeer', 'HEAD')",
		"CALL DOLT_CHECKOUT('bmpeer')",
		"UPDATE issues SET status = 'closed' WHERE id = 'bm-x'",
		"CALL DOLT_COMMIT('-am', 'peer closes blocker')",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout test branch: %v", err)
	}

	conflicts, err := store.Merge(ctx, "bmpeer")
	if err != nil {
		t.Fatalf("merge peer branch: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected conflict-free merge, got %d conflicts", len(conflicts))
	}

	if isBlocked(ctx, t, db, "bm-w") {
		t.Error("bm-w still is_blocked=1 after bd merge though the merged-in branch closed its only blocker")
	}
}

// TestRecomputeIsBlockedAfterMerge_WorkingSetMergeHEADUnchanged covers the
// bd-6dnrw.39 hole: a pull whose merge bd auto-resolved (conflicts) or
// cascade-repaired (FK violations) lands in the WORKING SET without advancing
// HEAD — the merge commit is only created by a later DOLT_COMMIT. The
// recompute must not mistake the unchanged HEAD for "nothing merged": it
// diffs to WORKING and skips only when issues/dependencies are clean too.
func TestRecomputeIsBlockedAfterMerge_WorkingSetMergeHEADUnchanged(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	preHead := seedBlockedPair(ctx, t, store, true)

	// The "merge": the remote's close of blocker X sits in the working set,
	// uncommitted — HEAD still equals the pre-pull commit.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("simulate resolved merge in working set: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("setup: bm-w must still read blocked before the recompute")
	}

	recomputeAfterMerge(ctx, t, store.db, preHead)

	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("bm-w still is_blocked=1: recompute skipped a working-set merge because HEAD was unchanged (bd-6dnrw.39)")
	}
}

// TestRecomputeIsBlockedAfterMerge_PreservesUpdatedAt pins bd-578h9.19:
// is_blocked is derived state, and issues.updated_at carries ON UPDATE
// CURRENT_TIMESTAMP, so a recompute that flips the flag must explicitly
// preserve updated_at. Otherwise every clone's post-pull recompute stamps
// its own wall clock into the synced issues table (cross-clone merge
// conflicts on rows that converged) and updated_at consumers (tracker
// conflict guard, import stale guard) misread the flip as a user edit.
func TestRecomputeIsBlockedAfterMerge_PreservesUpdatedAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	preHead := seedBlockedPair(ctx, t, store, true)

	// Pin bm-w's updated_at far in the past (explicit assignment suppresses
	// the ON UPDATE clause), so any bump by the recompute is unmissable.
	const pinned = "2026-01-01 00:00:00"
	if _, err := store.db.ExecContext(ctx,
		"UPDATE issues SET updated_at = ? WHERE id = 'bm-w'", pinned); err != nil {
		t.Fatalf("pin updated_at: %v", err)
	}
	// The remote closes the blocker; the recompute must flip bm-w to
	// unblocked WITHOUT touching its updated_at.
	if _, err := store.db.ExecContext(ctx,
		"UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("simulate merged close: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote closed bm-x')"); err != nil {
		t.Fatalf("commit merged close: %v", err)
	}

	recomputeAfterMerge(ctx, t, store.db, preHead)

	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("bm-w should be unblocked after its only blocker closed")
	}
	var got string
	if err := store.db.QueryRowContext(ctx,
		"SELECT CAST(updated_at AS CHAR) FROM issues WHERE id = 'bm-w'").Scan(&got); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if got != pinned {
		t.Errorf("recompute bumped derived-flag row's updated_at: got %s, want %s", got, pinned)
	}
}
