package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// leaseState reads an issue's claim state plus its lease-row overlay (the
// ephemeral leases table, bd-lrgn1) directly, for assertions. leaseExpires/
// heartbeatAt are NULL when the issue has no lease row.
type leaseState struct {
	status        string
	assignee      sql.NullString
	leaseExpires  sql.NullTime
	heartbeatAt   sql.NullTime
	rowLock       int64
	startedAt     sql.NullTime
	startedAtNull bool
}

func readLeaseState(t *testing.T, ctx context.Context, store *DoltStore, id string) leaseState {
	t.Helper()
	var ls leaseState
	var startedAt sql.NullTime
	err := store.db.QueryRowContext(ctx, `
		SELECT i.status, i.assignee, l.lease_expires_at, l.heartbeat_at, i.row_lock, i.started_at
		FROM issues i LEFT JOIN leases l ON l.issue_id = i.id
		WHERE i.id = ?
	`, id).Scan(&ls.status, &ls.assignee, &ls.leaseExpires, &ls.heartbeatAt, &ls.rowLock, &startedAt)
	if err != nil {
		t.Fatalf("read lease state for %s: %v", id, err)
	}
	ls.startedAt = startedAt
	ls.startedAtNull = !startedAt.Valid
	return ls
}

func seedClaimedIssue(t *testing.T, ctx context.Context, store *DoltStore, id, owner string, ttl time.Duration) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     "lease " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	claimCtx := issueops.WithLeaseTTL(ctx, ttl)
	if err := store.ClaimIssue(claimCtx, id, owner); err != nil {
		t.Fatalf("claim %s by %s: %v", id, owner, err)
	}
}

// TestClaimStampsLease verifies a claim sets a future lease, a heartbeat
// timestamp, and a non-zero row_lock.
func TestClaimStampsLease(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "lease-claim", "alice", time.Minute)
	ls := readLeaseState(t, ctx, store, "lease-claim")

	if ls.status != "in_progress" {
		t.Errorf("status = %q, want in_progress", ls.status)
	}
	if ls.assignee.String != "alice" {
		t.Errorf("assignee = %q, want alice", ls.assignee.String)
	}
	if !ls.leaseExpires.Valid || !ls.leaseExpires.Time.After(time.Now()) {
		t.Errorf("lease_expires_at = %v, want a future time", ls.leaseExpires)
	}
	if !ls.heartbeatAt.Valid {
		t.Error("heartbeat_at is NULL, want set on claim")
	}
	if ls.rowLock == 0 {
		t.Error("row_lock = 0, want a fresh non-zero value on claim")
	}
}

// TestHeartbeatExtendsLeaseAndGuardsOwnership verifies heartbeat pushes the
// lease forward and rewrites row_lock, that only the owner may heartbeat, and
// that a heartbeat on a closed issue fails.
func TestHeartbeatExtendsLeaseAndGuardsOwnership(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "lease-hb", "alice", time.Minute)
	before := readLeaseState(t, ctx, store, "lease-hb")

	time.Sleep(1100 * time.Millisecond) // DATETIME is second-granular; ensure a tick
	if err := store.HeartbeatIssue(ctx, "lease-hb", "alice"); err != nil {
		t.Fatalf("owner heartbeat: %v", err)
	}
	after := readLeaseState(t, ctx, store, "lease-hb")
	if !after.leaseExpires.Time.After(before.leaseExpires.Time) {
		t.Errorf("heartbeat did not extend lease: before=%v after=%v", before.leaseExpires.Time, after.leaseExpires.Time)
	}
	if after.rowLock != before.rowLock {
		t.Error("heartbeat touched the issues row (row_lock changed) — heartbeats must write only the leases table (bd-lrgn1)")
	}

	// A non-owner cannot heartbeat.
	if err := store.HeartbeatIssue(ctx, "lease-hb", "mallory"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("non-owner heartbeat err = %v, want ErrAlreadyClaimed", err)
	}

	// Once closed, the lease is gone — heartbeat fails.
	if err := store.CloseIssue(ctx, "lease-hb", "done", "alice", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := store.HeartbeatIssue(ctx, "lease-hb", "alice"); !errors.Is(err, storage.ErrNotClaimable) {
		t.Errorf("heartbeat after close err = %v, want ErrNotClaimable", err)
	}
	closed := readLeaseState(t, ctx, store, "lease-hb")
	if closed.leaseExpires.Valid || closed.heartbeatAt.Valid {
		t.Errorf("close did not clear lease columns: %+v", closed)
	}
}

func TestUpdateIssueMaintainsLeaseOwnership(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "lease-update", "alice", time.Hour)
	before := readLeaseState(t, ctx, store, "lease-update")

	// An assignee transfer through generic update clears the old owner's lease
	// rather than stamping a fresh one: the new holder did not claim through the
	// lease-aware verb, so their hold is durable until they opt in (bd-9hpgf).
	if err := store.UpdateIssue(ctx, "lease-update", map[string]interface{}{"assignee": "bob"}, "dispatcher"); err != nil {
		t.Fatalf("transfer assignee: %v", err)
	}
	transferred := readLeaseState(t, ctx, store, "lease-update")
	if transferred.assignee.String != "bob" {
		t.Fatalf("assignee after transfer = %q, want bob", transferred.assignee.String)
	}
	if transferred.leaseExpires.Valid || transferred.heartbeatAt.Valid {
		t.Fatalf("transfer should clear the old owner's lease, got %+v", transferred)
	}
	if transferred.rowLock == before.rowLock {
		t.Fatalf("transfer did not rewrite row_lock: before=%+v after=%+v", before, transferred)
	}
	if err := store.HeartbeatIssue(ctx, "lease-update", "alice"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("old owner heartbeat err = %v, want ErrAlreadyClaimed", err)
	}
	// The new owner can opt back into lease semantics by heartbeating.
	if err := store.HeartbeatIssue(ctx, "lease-update", "bob"); err != nil {
		t.Fatalf("new owner heartbeat: %v", err)
	}
	rearmed := readLeaseState(t, ctx, store, "lease-update")
	if !rearmed.leaseExpires.Valid || !rearmed.heartbeatAt.Valid {
		t.Fatalf("heartbeat by new owner should re-arm the lease, got %+v", rearmed)
	}

	if err := store.UpdateIssue(ctx, "lease-update", map[string]interface{}{"status": string(types.StatusOpen)}, "dispatcher"); err != nil {
		t.Fatalf("reopen through update: %v", err)
	}
	open := readLeaseState(t, ctx, store, "lease-update")
	if open.leaseExpires.Valid || open.heartbeatAt.Valid {
		t.Fatalf("status away from in_progress did not clear lease: %+v", open)
	}
}

// TestUnclaimOwnershipAndLease verifies that unclaim (a) rejects a non-owner
// with ErrNotOwner and leaves the claim intact, (b) when done by the owner
// clears the lease columns and rewrites row_lock so a racing heartbeat/reclaim
// conflicts, and (c) with force overrides the ownership check.
func TestUnclaimOwnershipAndLease(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// (a) Non-owner cannot release someone else's claim.
	seedClaimedIssue(t, ctx, store, "unclaim-a", "alice", time.Hour)
	before := readLeaseState(t, ctx, store, "unclaim-a")
	if err := store.UnclaimIssue(ctx, "unclaim-a", "mallory", false); !errors.Is(err, storage.ErrNotOwner) {
		t.Fatalf("non-owner unclaim err = %v, want ErrNotOwner", err)
	}
	stillClaimed := readLeaseState(t, ctx, store, "unclaim-a")
	if stillClaimed.status != "in_progress" || stillClaimed.assignee.String != "alice" {
		t.Fatalf("rejected unclaim mutated state: %+v", stillClaimed)
	}
	if stillClaimed.rowLock != before.rowLock {
		t.Fatalf("rejected unclaim rewrote row_lock: before=%d after=%d", before.rowLock, stillClaimed.rowLock)
	}

	// (b) Owner releases: status → open, assignee cleared, lease columns
	// cleared, row_lock rewritten.
	if err := store.UnclaimIssue(ctx, "unclaim-a", "alice", false); err != nil {
		t.Fatalf("owner unclaim: %v", err)
	}
	released := readLeaseState(t, ctx, store, "unclaim-a")
	if released.status != "open" {
		t.Errorf("status after unclaim = %q, want open", released.status)
	}
	if released.assignee.Valid && released.assignee.String != "" {
		t.Errorf("assignee after unclaim = %q, want empty", released.assignee.String)
	}
	if released.leaseExpires.Valid || released.heartbeatAt.Valid {
		t.Errorf("unclaim did not clear lease columns: %+v", released)
	}
	if !released.startedAtNull {
		t.Errorf("unclaim did not clear started_at: %+v", released)
	}
	if released.rowLock == before.rowLock || released.rowLock == 0 {
		t.Errorf("unclaim did not rewrite row_lock to a fresh non-zero value: before=%d after=%d", before.rowLock, released.rowLock)
	}

	// A heartbeat from the old owner after release finds no live claim.
	if err := store.HeartbeatIssue(ctx, "unclaim-a", "alice"); !errors.Is(err, storage.ErrNotClaimable) {
		t.Errorf("heartbeat after unclaim err = %v, want ErrNotClaimable", err)
	}

	// (c) --force lets an admin/reaper release a claim held by someone else.
	seedClaimedIssue(t, ctx, store, "unclaim-c", "alice", time.Hour)
	if err := store.UnclaimIssue(ctx, "unclaim-c", "reaper", true); err != nil {
		t.Fatalf("forced unclaim by non-owner: %v", err)
	}
	forced := readLeaseState(t, ctx, store, "unclaim-c")
	if forced.status != "open" || (forced.assignee.Valid && forced.assignee.String != "") {
		t.Fatalf("forced unclaim did not release: %+v", forced)
	}
	if forced.leaseExpires.Valid || forced.heartbeatAt.Valid {
		t.Fatalf("forced unclaim did not clear lease columns: %+v", forced)
	}
}

// TestBareUpdateClaimDoesNotArmLease is the regression guard for bd-9hpgf
// (GH#4716): a plain interactive claim — `bd update -s in_progress -a <who>`
// with no worker/lease semantics intended — must NOT arm a lease. Nobody
// heartbeats an interactive session, so an armed lease just lapses and the
// reclaim reaper reverts the bead out from under its owner.
func TestBareUpdateClaimDoesNotArmLease(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "lease-handdole",
		Title:     "hand-dole claim",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.UpdateIssue(ctx, "lease-handdole", map[string]interface{}{
		"status":   string(types.StatusInProgress),
		"assignee": "crow",
	}, "crow"); err != nil {
		t.Fatalf("bare interactive claim: %v", err)
	}
	ls := readLeaseState(t, ctx, store, "lease-handdole")
	if ls.status != "in_progress" || ls.assignee.String != "crow" {
		t.Fatalf("claim did not stick: %+v", ls)
	}
	if ls.leaseExpires.Valid || ls.heartbeatAt.Valid {
		t.Fatalf("bare status+assignee update armed a lease: %+v", ls)
	}

	// Even a reclaim sweep with its cutoff pushed into the future (which would
	// reap ANY leased issue) must leave the interactive claim alone: durable
	// until the actor releases it or a human reclaims.
	reclaimed, err := store.ReclaimExpiredLeases(ctx, -time.Hour, "reaper")
	if err != nil {
		t.Fatalf("reclaim sweep: %v", err)
	}
	for _, r := range reclaimed {
		if r.ID == "lease-handdole" {
			t.Fatalf("interactive claim was reclaimed: %+v", r)
		}
	}
	after := readLeaseState(t, ctx, store, "lease-handdole")
	if after.status != "in_progress" || after.assignee.String != "crow" {
		t.Fatalf("interactive claim disturbed by reclaim: %+v", after)
	}
}

// TestUpdatePreservesLeaseOnSameClaim: a generic update that does not change
// who holds the claim (same assignee, still in_progress) must leave a worker's
// live lease untouched — a supervisor tweaking priority or re-asserting the
// same status must not disarm dead-worker recovery for a real worker claim.
func TestUpdatePreservesLeaseOnSameClaim(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "lease-same", "alice", time.Hour)
	before := readLeaseState(t, ctx, store, "lease-same")
	if !before.leaseExpires.Valid {
		t.Fatalf("seed claim did not arm a lease: %+v", before)
	}

	// Re-assert the same status+assignee alongside an unrelated field edit.
	if err := store.UpdateIssue(ctx, "lease-same", map[string]interface{}{
		"status":   string(types.StatusInProgress),
		"assignee": "alice",
		"priority": 1,
	}, "supervisor"); err != nil {
		t.Fatalf("same-claim update: %v", err)
	}
	after := readLeaseState(t, ctx, store, "lease-same")
	if !after.leaseExpires.Valid || !after.heartbeatAt.Valid {
		t.Fatalf("same-claim update dropped the worker's lease: %+v", after)
	}
	if !after.leaseExpires.Time.Equal(before.leaseExpires.Time) {
		t.Errorf("same-claim update moved lease_expires_at: before=%v after=%v",
			before.leaseExpires.Time, after.leaseExpires.Time)
	}
}

// TestReclaimRevertsExpiredOnly verifies reclaim reverts an expired lease to
// ready (clearing the owner) and leaves a still-valid lease untouched, and that
// the grace window is honored.
func TestReclaimRevertsExpiredOnly(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// "dead" holds a 1s lease and never heartbeats; "live" holds a long lease.
	seedClaimedIssue(t, ctx, store, "lease-dead", "dead-worker", time.Second)
	seedClaimedIssue(t, ctx, store, "lease-live", "live-worker", time.Hour)

	time.Sleep(1500 * time.Millisecond) // let dead's lease expire

	// Grace window larger than how long the lease has been expired: nothing yet.
	reclaimed, err := store.ReclaimExpiredLeases(ctx, time.Hour, "reaper")
	if err != nil {
		t.Fatalf("reclaim with big grace: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("reclaimed %v with a 1h grace window, want none", reclaimed)
	}

	// Zero grace: the dead lease (expired) is reclaimed, the live one is not.
	reclaimed, err = store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim grace=0: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "lease-dead" {
		t.Fatalf("reclaimed = %+v, want exactly [lease-dead]", reclaimed)
	}
	if reclaimed[0].PreviousOwner != "dead-worker" {
		t.Errorf("PreviousOwner = %q, want dead-worker", reclaimed[0].PreviousOwner)
	}

	dead := readLeaseState(t, ctx, store, "lease-dead")
	if dead.status != "open" || dead.assignee.Valid && dead.assignee.String != "" {
		t.Errorf("reclaimed issue state = %+v, want open + unassigned", dead)
	}
	if dead.leaseExpires.Valid || dead.heartbeatAt.Valid || !dead.startedAtNull {
		t.Errorf("reclaim did not clear lease/started columns: %+v", dead)
	}
	live := readLeaseState(t, ctx, store, "lease-live")
	if live.status != "in_progress" || live.assignee.String != "live-worker" {
		t.Errorf("live issue disturbed by reclaim: %+v", live)
	}

	// A recovery event was recorded for the reclaimed issue.
	var events int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = 'lease_reclaimed'`, "lease-dead").Scan(&events); err != nil {
		t.Fatalf("count reclaim events: %v", err)
	}
	if events != 1 {
		t.Errorf("lease_reclaimed events = %d, want 1", events)
	}
}

// TestRowLockForcesConflictOnDisjointCellWrites is the regression guard for the
// row_lock trick. It shows, against real Dolt, that two concurrent transactions
// writing DISJOINT cells of the same issue row silently cell-merge — UNLESS
// both also rewrite the shared row_lock cell, which forces the second commit
// to conflict (1213) so withRetryTx can replay.
//
// Since bd-lrgn1 heartbeats no longer touch the issues row (they live on the
// leases table, where a racing reclaim contends on the SAME row — see
// TestHeartbeatReclaimContendOnLeaseRow). The disjoint-cell pair guarded here
// is a worker-side field edit (e.g. priority, via updateIssueInTx) racing a
// reaper reverting the row to ready (status/assignee): without the shared
// lock cell Dolt merges them silently, so the edit path never learns its row
// was reverted underneath it.
func TestRowLockForcesConflictOnDisjointCellWrites(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// runRace commits two concurrent disjoint-cell writers. withLock adds the
	// shared row_lock cell to both. Returns the error from the second commit.
	runRace := func(id string, withLock bool) error {
		seedClaimedIssue(t, ctx, store, id, "owner", time.Hour)

		tx1, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tx1: %v", err)
		}
		tx2, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tx2: %v", err)
		}

		editSQL := "UPDATE issues SET priority = 0 WHERE id = ?"
		reclaimSQL := "UPDATE issues SET status = 'open', assignee = NULL WHERE id = ?"
		editArgs := []any{id}
		reclaimArgs := []any{id}
		if withLock {
			editSQL = "UPDATE issues SET priority = 0, row_lock = ? WHERE id = ?"
			editArgs = []any{int64(111111), id}
			reclaimSQL = "UPDATE issues SET status = 'open', assignee = NULL, row_lock = ? WHERE id = ?"
			reclaimArgs = []any{int64(222222), id}
		}

		if _, err := tx1.ExecContext(ctx, editSQL, editArgs...); err != nil {
			t.Fatalf("tx1 edit exec: %v", err)
		}
		if _, err := tx2.ExecContext(ctx, reclaimSQL, reclaimArgs...); err != nil {
			t.Fatalf("tx2 reclaim exec: %v", err)
		}
		// Commit the edit first, then the reclaim. The reclaim is the loser
		// that must conflict when both touch row_lock.
		if err := tx1.Commit(); err != nil {
			t.Fatalf("tx1 commit (edit): %v", err)
		}
		return tx2.Commit()
	}

	// WITHOUT row_lock: the disjoint writes merge with no error.
	if err := runRace("zombie-norowlock", false); err != nil {
		// A conflict here would also be acceptable (still safe), but the point of
		// the test is to demonstrate the silent merge, so surface if Dolt's
		// behavior changed.
		t.Skipf("expected silent merge without row_lock, got conflict %v (Dolt merge semantics changed)", err)
	}
	var priority int
	var status string
	if err := store.db.QueryRowContext(ctx,
		"SELECT priority, status FROM issues WHERE id = ?", "zombie-norowlock").Scan(&priority, &status); err != nil {
		t.Fatalf("read merged row: %v", err)
	}
	if priority != 0 || status != "open" {
		t.Fatalf("without row_lock expected a silent merge (priority=0 AND status=open), got priority=%d status=%s", priority, status)
	}
	t.Logf("without row_lock: cell-merge landed both writes (priority=%d, status=%s)", priority, status)

	// WITH row_lock: both writers touch the shared cell, so the second commit
	// conflicts (1213/1205) instead of silently merging.
	err := runRace("zombie-rowlock", true)
	if err == nil {
		t.Fatal("with row_lock expected the second commit to conflict, but it succeeded (silent merge not prevented)")
	}
	if !isSerializationError(err) {
		t.Fatalf("with row_lock got err %v, want a serialization conflict (1213/1205)", err)
	}
	t.Logf("with row_lock: second commit correctly conflicted: %v", err)
}

// TestHeartbeatReclaimContendOnLeaseRow pins the property that replaced the
// old heartbeat-vs-reaper row_lock guard (bd-lrgn1): a heartbeat (UPDATE of
// the lease row) and a reclaim (DELETE of the same lease row) contend on the
// SAME leases row, so Dolt's commit-time merge forces the second committer to
// conflict — no shared lock cell needed. The loser's withRetryTx replay then
// sees the winner's state: a replayed heartbeat finds its lease gone (worker
// learns it lost the claim), a replayed reclaim finds a fresh expiry and
// leaves the rescued claim alone.
func TestHeartbeatReclaimContendOnLeaseRow(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "lease-contend", "owner", time.Hour)

	tx1, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	tx2, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}

	// tx1: heartbeat pushes the expiry forward. tx2: reclaim deletes the row.
	if _, err := tx1.ExecContext(ctx,
		"UPDATE leases SET lease_expires_at = ?, heartbeat_at = ? WHERE issue_id = ?",
		time.Now().UTC().Add(2*time.Hour), time.Now().UTC(), "lease-contend"); err != nil {
		t.Fatalf("tx1 heartbeat exec: %v", err)
	}
	if _, err := tx2.ExecContext(ctx,
		"DELETE FROM leases WHERE issue_id = ?", "lease-contend"); err != nil {
		t.Fatalf("tx2 reclaim exec: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit (heartbeat): %v", err)
	}
	err = tx2.Commit()
	if err == nil {
		t.Fatal("update-vs-delete of the same lease row did not conflict — the heartbeat/reclaim race guard is gone")
	}
	if !isSerializationError(err) {
		t.Fatalf("lease-row contention err = %v, want a serialization conflict (1213/1205)", err)
	}
	t.Logf("lease-row update-vs-delete correctly conflicted: %v", err)
}

// TestHeartbeatMintsNoDoltCommits is acceptance criterion (1) of bd-lrgn1: a
// claim followed by N heartbeats produces exactly the claim's own commit and
// ZERO further commits — heartbeats write only the dolt_ignored leases table.
// Status/assignee transitions (claim, close) still commit (criterion 2).
func TestHeartbeatMintsNoDoltCommits(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	commitCount := func() int {
		var n int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		return n
	}

	seedClaimedIssue(t, ctx, store, "lease-nocommit", "alice", time.Hour)
	afterClaim := commitCount()

	for i := 0; i < 5; i++ {
		if err := store.HeartbeatIssue(ctx, "lease-nocommit", "alice"); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
	}
	if got := commitCount(); got != afterClaim {
		t.Errorf("heartbeats minted %d dolt commit(s) — want zero (leases are dolt_ignored)", got-afterClaim)
	}

	// The lease is genuinely renewed even though nothing committed.
	ls := readLeaseState(t, ctx, store, "lease-nocommit")
	if !ls.leaseExpires.Valid || !ls.leaseExpires.Time.After(time.Now()) {
		t.Errorf("lease not renewed after heartbeats: %+v", ls)
	}

	// A status transition still commits: close mints a commit.
	if err := store.CloseIssue(ctx, "lease-nocommit", "done", "alice", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := commitCount(); got <= afterClaim {
		t.Errorf("close minted no dolt commit: count %d, want > %d", got, afterClaim)
	}
}

// TestConcurrentHeartbeatReclaimClose is the integration race (the lease analog
// of TestConcurrentWorkQueueDrain). Half the workers are "live" (heartbeat, then
// close their issue); half are "dead" (claim, then go silent). A reaper runs
// continuously with a zero grace window. The store-level withRetryTx + row_lock
// must keep every issue in a consistent terminal state:
//
//   - a live worker's close is never lost and never reverted by a racing reclaim
//   - a dead worker's issue is recovered to ready (open, unassigned, no lease)
//   - no issue is ever a zombie: open with a lingering owner/lease, or closed
//     with a lingering owner/lease
func TestConcurrentHeartbeatReclaimClose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lease recovery race in short mode")
	}
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const numWorkers = 8 // even split live/dead
	// lease_expires_at/heartbeat_at are second-granular DATETIME columns (and
	// Dolt ROUNDS, not truncates), so a sub-second TTL is meaningless — it can
	// round to a whole second in the future. Use second-scale timings: a 2s
	// lease that live workers refresh every 500ms (always ≥1.5s in the future,
	// so the reaper never touches a live claim), while dead claims expire after
	// 2s and get reclaimed.
	const ttl = 2 * time.Second
	type job struct {
		id    string
		owner string
		live  bool
	}
	jobs := make([]job, numWorkers)
	for i := 0; i < numWorkers; i++ {
		j := job{
			id:    fmt.Sprintf("lease-race-%02d", i),
			owner: fmt.Sprintf("worker-%02d", i),
			live:  i%2 == 0,
		}
		jobs[i] = j
		seedClaimedIssue(t, ctx, store, j.id, j.owner, ttl)
	}

	raceCtx, stopReaper := context.WithCancel(ctx)
	var reaperErrs atomic.Int32
	var reaperDone sync.WaitGroup
	reaperDone.Add(1)
	go func() {
		defer reaperDone.Done()
		for {
			select {
			case <-raceCtx.Done():
				return
			default:
			}
			if _, err := store.ReclaimExpiredLeases(raceCtx, 0, "reaper"); err != nil {
				if raceCtx.Err() == nil {
					reaperErrs.Add(1)
				}
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
	}()

	var wg sync.WaitGroup
	var lostClose atomic.Int32
	for _, j := range jobs {
		if !j.live {
			continue // dead workers do nothing; the reaper recovers them
		}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			hbCtx := issueops.WithLeaseTTL(ctx, ttl)
			// Heartbeat every 500ms (well within the 2s TTL) to keep the lease
			// alive while the reaper scans concurrently, then close. Each heartbeat
			// and close rewrites row_lock, so they race the reaper's row_lock
			// rewrites; withRetryTx must absorb any conflict.
			for k := 0; k < 4; k++ {
				if err := store.HeartbeatIssue(hbCtx, j.id, j.owner); err != nil {
					// A live worker keeps its lease well in the future, so the
					// reaper must never preempt it — a failure is a real defect.
					t.Errorf("live worker %s heartbeat failed: %v", j.owner, err)
				}
				time.Sleep(500 * time.Millisecond)
			}
			if err := store.CloseIssue(ctx, j.id, "done", j.owner, ""); err != nil {
				// A close can only fail here if the reaper reverted the issue to
				// open and another path re-touched it; with row_lock the close
				// retries and wins, so a hard failure is a real defect.
				t.Errorf("live worker %s close failed: %v", j.owner, err)
				lostClose.Add(1)
			}
		}(j)
	}
	wg.Wait()
	stopReaper()
	reaperDone.Wait()

	if n := reaperErrs.Load(); n != 0 {
		t.Errorf("reaper surfaced %d errors; ReclaimExpiredLeases should retry conflicts internally", n)
	}
	if n := lostClose.Load(); n != 0 {
		t.Errorf("%d live-worker closes were lost to a racing reclaim", n)
	}

	// Final consistency sweep: wait past the TTL (plus the ≤1s DATETIME rounding
	// slop) so every dead claim's lease is unambiguously expired, then drain so
	// dead workers' issues reach their terminal open state regardless of reaper
	// timing.
	time.Sleep(ttl + 1500*time.Millisecond)
	if _, err := store.ReclaimExpiredLeases(ctx, 0, "final-reaper"); err != nil {
		t.Fatalf("final reclaim sweep: %v", err)
	}

	closed, open := 0, 0
	for _, j := range jobs {
		ls := readLeaseState(t, ctx, store, j.id)
		hasOwner := ls.assignee.Valid && ls.assignee.String != ""
		switch ls.status {
		case "closed":
			closed++
			// A closed issue must carry no live lease (close clears it).
			if ls.leaseExpires.Valid || ls.heartbeatAt.Valid {
				t.Errorf("issue %s closed but still leased: %+v", j.id, ls)
			}
		case "open":
			open++
			// A reclaimed issue must be fully released: no owner, no lease.
			if hasOwner || ls.leaseExpires.Valid || ls.heartbeatAt.Valid {
				t.Errorf("issue %s open but still owned/leased (zombie): %+v", j.id, ls)
			}
		case "in_progress":
			t.Errorf("issue %s still in_progress after race settled: %+v", j.id, ls)
		default:
			t.Errorf("issue %s unexpected status %q", j.id, ls.status)
		}
	}
	t.Logf("lease race settled: %d closed (live), %d open (reclaimed)", closed, open)
	// Every live issue should have ended closed; every dead one open.
	if closed == 0 || open == 0 {
		t.Errorf("expected a mix of closed (live) and open (reclaimed) issues, got closed=%d open=%d", closed, open)
	}
}

// expiryMargin is how long a test waits for a shortLeaseTTL lease to become
// reclaimable. Dolt's DATETIME columns hold whole seconds and round on write,
// so conformance tests must stay clear of sub-second lease behavior (L1.3).
const (
	shortLeaseTTL = time.Second
	expiryMargin  = 2500 * time.Millisecond
)

// TestStartedAtStampedOnceAcrossClaims pins L2.2: claims set started_at only
// when it is absent. Idempotent claims and re-claims after a status-only reopen
// preserve the original value. Reaper reclaim remains the deliberate contrast:
// L5.2 clears started_at, so the next claimant receives a fresh start time.
func TestStartedAtStampedOnceAcrossClaims(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "started-keep", "alice", time.Hour)
	first := readLeaseState(t, ctx, store, "started-keep")
	if first.startedAtNull {
		t.Fatal("claim did not stamp started_at")
	}
	origin := first.startedAt.Time

	time.Sleep(1100 * time.Millisecond) // DATETIME is second-granular
	if err := store.ClaimIssue(ctx, "started-keep", "alice"); err != nil {
		t.Fatalf("idempotent re-claim: %v", err)
	}
	if got := readLeaseState(t, ctx, store, "started-keep").startedAt.Time; !got.Equal(origin) {
		t.Errorf("idempotent re-claim moved started_at: %v -> %v", origin, got)
	}

	// A status-only reopen keeps both the assignee and started_at. Re-claiming
	// therefore exercises the COALESCE(started_at, now) preservation branch.
	if err := store.UpdateIssue(ctx, "started-keep", map[string]interface{}{"status": string(types.StatusOpen)}, "dispatcher"); err != nil {
		t.Fatalf("reopen through update: %v", err)
	}
	reopened := readLeaseState(t, ctx, store, "started-keep")
	if reopened.assignee.String != "alice" {
		t.Errorf("status-only update cleared the assignee: %q", reopened.assignee.String)
	}
	if reopened.startedAtNull || !reopened.startedAt.Time.Equal(origin) {
		t.Fatalf("reopen moved started_at: %v -> %+v", origin, reopened.startedAt)
	}

	time.Sleep(1100 * time.Millisecond)
	if err := store.ClaimIssue(ctx, "started-keep", "alice"); err != nil {
		t.Fatalf("re-claim after reopen: %v", err)
	}
	after := readLeaseState(t, ctx, store, "started-keep")
	if after.status != "in_progress" || !after.startedAt.Time.Equal(origin) {
		t.Errorf("re-claim did not preserve started_at %v: %+v", origin, after)
	}

	// Reclaim is explicitly different: L5.2 clears started_at, and the next
	// claim starts a new work interval.
	seedClaimedIssue(t, ctx, store, "started-reclaim", "dead-worker", shortLeaseTTL)
	dead := readLeaseState(t, ctx, store, "started-reclaim")
	if dead.startedAtNull {
		t.Fatal("claim did not stamp started_at on started-reclaim")
	}
	deadStart := dead.startedAt.Time
	time.Sleep(expiryMargin)
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "started-reclaim" {
		t.Fatalf("reclaimed = %+v, want exactly [started-reclaim]", reclaimed)
	}
	if !readLeaseState(t, ctx, store, "started-reclaim").startedAtNull {
		t.Fatal("reclaim did not clear started_at (L5.2)")
	}
	if err := store.ClaimIssue(ctx, "started-reclaim", "bob"); err != nil {
		t.Fatalf("claim after reclaim: %v", err)
	}
	fresh := readLeaseState(t, ctx, store, "started-reclaim")
	if fresh.startedAtNull || !fresh.startedAt.Time.After(deadStart) {
		t.Errorf("claim after reclaim did not stamp a fresh start: previous=%v current=%+v", deadStart, fresh.startedAt)
	}
}

// TestHeartbeatRevivesExpiredLease pins L4.3: expiry alone transfers nothing.
// The owner may revive an expired lease until reclaim actually returns the issue
// to the pool; after reclaim, the old owner's heartbeat is not claimable.
func TestHeartbeatRevivesExpiredLease(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "lease-revive", "alice", shortLeaseTTL)
	seedClaimedIssue(t, ctx, store, "lease-lost", "bob", shortLeaseTTL)
	time.Sleep(expiryMargin)

	expired := readLeaseState(t, ctx, store, "lease-revive")
	if !expired.leaseExpires.Valid || expired.leaseExpires.Time.After(time.Now()) {
		t.Fatalf("precondition: lease-revive should be expired, got %v", expired.leaseExpires)
	}
	if err := store.HeartbeatIssue(ctx, "lease-revive", "alice"); err != nil {
		t.Fatalf("owner heartbeat on expired unreclaimed lease: %v", err)
	}
	revived := readLeaseState(t, ctx, store, "lease-revive")
	if !revived.leaseExpires.Valid || !revived.leaseExpires.Time.After(time.Now()) {
		t.Errorf("heartbeat did not revive lease: %v", revived.leaseExpires)
	}

	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim grace=0: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "lease-lost" {
		t.Fatalf("reclaimed = %+v, want exactly [lease-lost]", reclaimed)
	}
	if still := readLeaseState(t, ctx, store, "lease-revive"); still.status != "in_progress" || still.assignee.String != "alice" {
		t.Errorf("reaper disturbed revived claim: %+v", still)
	}
	if err := store.HeartbeatIssue(ctx, "lease-lost", "bob"); !errors.Is(err, storage.ErrNotClaimable) {
		t.Errorf("heartbeat after reclaim err = %v, want ErrNotClaimable", err)
	}
}

// TestConcurrentReapersAreBenign pins L5.5: two reapers racing the same stale
// set neither error nor double-report a reclaim, and do not disturb live work.
func TestConcurrentReapersAreBenign(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const staleCount = 4
	stale := make([]string, 0, staleCount)
	for i := 0; i < staleCount; i++ {
		id := fmt.Sprintf("reapers-stale-%d", i)
		seedClaimedIssue(t, ctx, store, id, fmt.Sprintf("dead-%d", i), shortLeaseTTL)
		stale = append(stale, id)
	}
	seedClaimedIssue(t, ctx, store, "reapers-live", "live-worker", time.Hour)
	time.Sleep(expiryMargin)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = map[string][]string{}
	)
	for _, reaper := range []string{"reaper-a", "reaper-b"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			got, err := store.ReclaimExpiredLeases(ctx, 0, actor)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("%s surfaced reclaim error: %v", actor, err)
				return
			}
			for _, r := range got {
				results[actor] = append(results[actor], r.ID)
			}
		}(reaper)
	}
	wg.Wait()

	seen := map[string]string{}
	for actor, ids := range results {
		for _, id := range ids {
			if previous, duplicate := seen[id]; duplicate {
				t.Errorf("%s reported by both %s and %s", id, previous, actor)
			}
			seen[id] = actor
		}
	}
	for _, id := range stale {
		if _, ok := seen[id]; !ok {
			t.Errorf("stale issue %s was not reclaimed", id)
		}
		ls := readLeaseState(t, ctx, store, id)
		if ls.status != "open" || (ls.assignee.Valid && ls.assignee.String != "") || ls.leaseExpires.Valid || ls.heartbeatAt.Valid || !ls.startedAtNull {
			t.Errorf("%s left in inconsistent state: %+v", id, ls)
		}
		var events int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = 'lease_reclaimed'`, id).Scan(&events); err != nil {
			t.Fatalf("count reclaim events for %s: %v", id, err)
		}
		if events != 1 {
			t.Errorf("%s lease_reclaimed events = %d, want 1", id, events)
		}
	}
	if actor := seen["reapers-live"]; actor != "" {
		t.Errorf("live issue was reclaimed by %s", actor)
	}
	if live := readLeaseState(t, ctx, store, "reapers-live"); live.status != "in_progress" || live.assignee.String != "live-worker" {
		t.Errorf("live issue disturbed: %+v", live)
	}
}
