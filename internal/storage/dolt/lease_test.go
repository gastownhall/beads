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

// leaseState reads the lease columns for an issue directly, for assertions.
type leaseState struct {
	status        string
	assignee      sql.NullString
	leaseExpires  sql.NullTime
	heartbeatAt   sql.NullTime
	rowLock       int64
	startedAtNull bool
}

func readLeaseState(t *testing.T, ctx context.Context, store *DoltStore, id string) leaseState {
	t.Helper()
	var ls leaseState
	var startedAt sql.NullTime
	err := store.db.QueryRowContext(ctx, `
		SELECT status, assignee, lease_expires_at, heartbeat_at, row_lock, started_at
		FROM issues WHERE id = ?
	`, id).Scan(&ls.status, &ls.assignee, &ls.leaseExpires, &ls.heartbeatAt, &ls.rowLock, &startedAt)
	if err != nil {
		t.Fatalf("read lease state for %s: %v", id, err)
	}
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
	if after.rowLock == before.rowLock {
		t.Error("heartbeat did not rewrite row_lock")
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

	if err := store.UpdateIssue(ctx, "lease-update", map[string]interface{}{"assignee": "bob"}, "dispatcher"); err != nil {
		t.Fatalf("transfer assignee: %v", err)
	}
	transferred := readLeaseState(t, ctx, store, "lease-update")
	if transferred.assignee.String != "bob" {
		t.Fatalf("assignee after transfer = %q, want bob", transferred.assignee.String)
	}
	if !transferred.leaseExpires.Valid || !transferred.heartbeatAt.Valid {
		t.Fatalf("transfer did not stamp a fresh lease: %+v", transferred)
	}
	if !transferred.heartbeatAt.Time.After(before.heartbeatAt.Time) && transferred.rowLock == before.rowLock {
		t.Fatalf("transfer did not refresh heartbeat or row_lock: before=%+v after=%+v", before, transferred)
	}
	if err := store.HeartbeatIssue(ctx, "lease-update", "alice"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("old owner heartbeat err = %v, want ErrAlreadyClaimed", err)
	}
	if err := store.HeartbeatIssue(ctx, "lease-update", "bob"); err != nil {
		t.Fatalf("new owner heartbeat: %v", err)
	}

	if err := store.UpdateIssue(ctx, "lease-update", map[string]interface{}{"status": string(types.StatusOpen)}, "dispatcher"); err != nil {
		t.Fatalf("reopen through update: %v", err)
	}
	open := readLeaseState(t, ctx, store, "lease-update")
	if open.leaseExpires.Valid || open.heartbeatAt.Valid {
		t.Fatalf("status away from in_progress did not clear lease: %+v", open)
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
// writing DISJOINT cells of the same issue row silently cell-merge into a
// corrupt "zombie" state — UNLESS both also rewrite the shared row_lock cell,
// which forces the second commit to conflict (1213) so withRetryTx can replay.
//
// The dangerous pair: a worker's heartbeat (writes heartbeat_at) racing a reaper
// reverting the row to ready (writes status/assignee). Without a shared lock
// cell Dolt merges them, producing an open+unassigned issue that still carries
// the worker's fresh heartbeat — the worker believes it owns work that another
// worker can now also claim.
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

		hbSQL := "UPDATE issues SET heartbeat_at = ? WHERE id = ?"
		reclaimSQL := "UPDATE issues SET status = 'open', assignee = NULL WHERE id = ?"
		hbArgs := []any{time.Now().UTC(), id}
		reclaimArgs := []any{id}
		if withLock {
			hbSQL = "UPDATE issues SET heartbeat_at = ?, row_lock = ? WHERE id = ?"
			hbArgs = []any{time.Now().UTC(), int64(111111), id}
			reclaimSQL = "UPDATE issues SET status = 'open', assignee = NULL, row_lock = ? WHERE id = ?"
			reclaimArgs = []any{int64(222222), id}
		}

		if _, err := tx1.ExecContext(ctx, hbSQL, hbArgs...); err != nil {
			t.Fatalf("tx1 heartbeat exec: %v", err)
		}
		if _, err := tx2.ExecContext(ctx, reclaimSQL, reclaimArgs...); err != nil {
			t.Fatalf("tx2 reclaim exec: %v", err)
		}
		// Commit the heartbeat first, then the reclaim. The reclaim is the loser
		// that must conflict when both touch row_lock.
		if err := tx1.Commit(); err != nil {
			t.Fatalf("tx1 commit (heartbeat): %v", err)
		}
		return tx2.Commit()
	}

	// WITHOUT row_lock: the disjoint writes merge with no error, leaving a zombie.
	if err := runRace("zombie-norowlock", false); err != nil {
		// A conflict here would also be acceptable (still safe), but the point of
		// the test is to demonstrate the silent merge, so surface if Dolt's
		// behavior changed.
		t.Skipf("expected silent merge without row_lock, got conflict %v (Dolt merge semantics changed)", err)
	}
	z := readLeaseState(t, ctx, store, "zombie-norowlock")
	zombie := z.status == "open" && z.heartbeatAt.Valid && (!z.assignee.Valid || z.assignee.String == "")
	if !zombie {
		t.Fatalf("without row_lock expected a merged zombie (open+unassigned+heartbeat), got %+v", z)
	}
	t.Logf("without row_lock: cell-merge produced zombie state %+v", z)

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
