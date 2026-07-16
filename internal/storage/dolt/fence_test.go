package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// fenceState reads the fence and its paired concurrency cell directly.
type fenceState struct {
	fence   int64
	rowLock int64
}

func readFenceState(t *testing.T, ctx context.Context, store *DoltStore, id string) fenceState {
	t.Helper()
	var fs fenceState
	err := store.db.QueryRowContext(ctx, `
		SELECT claim_fence, row_lock FROM issues WHERE id = ?
	`, id).Scan(&fs.fence, &fs.rowLock)
	if err != nil {
		t.Fatalf("read fence state for %s: %v", id, err)
	}
	return fs
}

func seedOpenIssue(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     "fence " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// assertFenceBumped asserts the fence advanced by exactly one and row_lock was
// rewritten in the same mutation — the pairing invariant that keeps a
// monotonic cell from silently cell-merging under Dolt (two concurrent N→N+1
// bumps write identical cells; only the random row_lock forces the conflict).
func assertFenceBumped(t *testing.T, before, after fenceState, what string) {
	t.Helper()
	if after.fence != before.fence+1 {
		t.Errorf("%s: claim_fence = %d, want %d (bump by exactly one)", what, after.fence, before.fence+1)
	}
	if after.rowLock == before.rowLock {
		t.Errorf("%s: row_lock unchanged (%d) — every fence bump must rewrite row_lock in the same statement", what, after.rowLock)
	}
}

// TestClaimBumpsFence: a fresh row starts at fence 0; claiming it is an
// ownership transition and bumps to 1.
func TestClaimBumpsFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedOpenIssue(t, ctx, store, "fence-claim")
	before := readFenceState(t, ctx, store, "fence-claim")
	if before.fence != 0 {
		t.Fatalf("pristine claim_fence = %d, want 0", before.fence)
	}

	if err := store.ClaimIssue(ctx, "fence-claim", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	assertFenceBumped(t, before, readFenceState(t, ctx, store, "fence-claim"), "claim")
}

// TestSameOwnerReclaimDoesNotBumpFence: the idempotent same-actor re-claim is
// a no-write success and must not advance the ownership context.
func TestSameOwnerReclaimDoesNotBumpFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "fence-idem", "alice", time.Hour)
	before := readFenceState(t, ctx, store, "fence-idem")

	if err := store.ClaimIssue(ctx, "fence-idem", "alice"); err != nil {
		t.Fatalf("idempotent re-claim: %v", err)
	}
	after := readFenceState(t, ctx, store, "fence-idem")
	if after.fence != before.fence {
		t.Errorf("idempotent re-claim moved claim_fence %d → %d, want unchanged", before.fence, after.fence)
	}
}

// TestUnclaimBumpsFence: release is an ownership transition.
func TestUnclaimBumpsFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "fence-unclaim", "alice", time.Hour)
	before := readFenceState(t, ctx, store, "fence-unclaim")

	if err := store.UnclaimIssue(ctx, "fence-unclaim", "alice", false); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	assertFenceBumped(t, before, readFenceState(t, ctx, store, "fence-unclaim"), "unclaim")
}

// TestReclaimBumpsFence: a lease reclaim takes ownership away and must fence
// out the previous holder.
func TestReclaimBumpsFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "fence-reclaim", "alice", time.Second)
	before := readFenceState(t, ctx, store, "fence-reclaim")
	// lease_expires_at is second-granular DATETIME; sleep well past the 1s TTL
	// so rounding can never leave the lease looking unexpired at the cutoff.
	time.Sleep(3 * time.Second)

	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed %d issues, want 1", len(reclaimed))
	}
	assertFenceBumped(t, before, readFenceState(t, ctx, store, "fence-reclaim"), "reclaim")
}

// TestPlainUpdateDoesNotBumpFence: a content mutation on a claimed row is not
// an ownership transition.
func TestPlainUpdateDoesNotBumpFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "fence-plain", "alice", time.Hour)
	before := readFenceState(t, ctx, store, "fence-plain")

	if err := store.UpdateIssue(ctx, "fence-plain", map[string]interface{}{"notes": "still mine"}, "alice"); err != nil {
		t.Fatalf("update notes: %v", err)
	}
	after := readFenceState(t, ctx, store, "fence-plain")
	if after.fence != before.fence {
		t.Errorf("plain update moved claim_fence %d → %d, want unchanged", before.fence, after.fence)
	}
}

// TestAssigneeChangeUpdateBumpsFence: reassignment through the generic update
// path is an ownership transition.
func TestAssigneeChangeUpdateBumpsFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "fence-reassign", "alice", time.Hour)
	before := readFenceState(t, ctx, store, "fence-reassign")

	if err := store.UpdateIssue(ctx, "fence-reassign", map[string]interface{}{"assignee": "bob"}, "admin"); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	assertFenceBumped(t, before, readFenceState(t, ctx, store, "fence-reassign"), "assignee-change update")
}

// TestReopenBumpsFence: reopening a closed row resets its ownership context —
// both through the dedicated verb and through the generic status update that
// the dolt/embedded stores actually use for reopen.
func TestReopenBumpsFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Via generic update (the primary path on this store).
	seedClaimedIssue(t, ctx, store, "fence-reopen-upd", "alice", time.Hour)
	if err := store.CloseIssue(ctx, "fence-reopen-upd", "done", "alice", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed := readFenceState(t, ctx, store, "fence-reopen-upd")
	if err := store.UpdateIssue(ctx, "fence-reopen-upd", map[string]interface{}{"status": "open"}, "admin"); err != nil {
		t.Fatalf("reopen via update: %v", err)
	}
	assertFenceBumped(t, closed, readFenceState(t, ctx, store, "fence-reopen-upd"), "reopen via update")

	// Via the dedicated verb (proxied-path parity).
	seedClaimedIssue(t, ctx, store, "fence-reopen-verb", "alice", time.Hour)
	if err := store.CloseIssue(ctx, "fence-reopen-verb", "done", "alice", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	closedVerb := readFenceState(t, ctx, store, "fence-reopen-verb")
	if err := store.ReopenIssue(ctx, "fence-reopen-verb", "not done after all", "admin"); err != nil {
		t.Fatalf("reopen verb: %v", err)
	}
	assertFenceBumped(t, closedVerb, readFenceState(t, ctx, store, "fence-reopen-verb"), "reopen verb")
}

// TestCloseDoesNotBumpFence: close is not a fence transition — guarded verbs
// protect against closed rows through their status predicates, and bumping on
// close would invalidate legitimate orchestrator snapshots for no gain.
func TestCloseDoesNotBumpFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "fence-close", "alice", time.Hour)
	before := readFenceState(t, ctx, store, "fence-close")

	if err := store.CloseIssue(ctx, "fence-close", "done", "alice", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	after := readFenceState(t, ctx, store, "fence-close")
	if after.fence != before.fence {
		t.Errorf("close moved claim_fence %d → %d, want unchanged", before.fence, after.fence)
	}
}

// TestFenceScanPosition: value-level sentinel test that the trailing columns
// land in the right Issue fields — a const-equality parity test alone would
// pass a silent claim_fence↔revision swap when two branches append trailing
// BIGINTs in different orders.
func TestFenceScanPosition(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedOpenIssue(t, ctx, store, "fence-scan")
	lease := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	heartbeat := time.Date(2032, 6, 7, 8, 9, 10, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, `
		UPDATE issues SET lease_expires_at = ?, heartbeat_at = ?, claim_fence = 7777 WHERE id = ?
	`, lease, heartbeat, "fence-scan"); err != nil {
		t.Fatalf("stamp sentinels: %v", err)
	}

	got, err := store.GetIssue(ctx, "fence-scan")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClaimFence != 7777 {
		t.Errorf("ClaimFence = %d, want sentinel 7777 (trailing-column position swap?)", got.ClaimFence)
	}
	if got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.Equal(lease) {
		t.Errorf("LeaseExpiresAt = %v, want %v", got.LeaseExpiresAt, lease)
	}
	if got.HeartbeatAt == nil || !got.HeartbeatAt.Equal(heartbeat) {
		t.Errorf("HeartbeatAt = %v, want %v", got.HeartbeatAt, heartbeat)
	}
}

// TestWispClaimBumpsFence: the fence is tier-complete — wisp-table rows carry
// and bump it exactly like permanent issues.
func TestWispClaimBumpsFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "fence-wisp",
		Title:     "fence wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("seed wisp: %v", err)
	}
	var before int64
	if err := store.db.QueryRowContext(ctx, `SELECT claim_fence FROM wisps WHERE id = ?`, "fence-wisp").Scan(&before); err != nil {
		t.Fatalf("read wisp fence: %v", err)
	}

	if err := store.ClaimIssue(ctx, "fence-wisp", "alice"); err != nil {
		t.Fatalf("claim wisp: %v", err)
	}
	var after int64
	if err := store.db.QueryRowContext(ctx, `SELECT claim_fence FROM wisps WHERE id = ?`, "fence-wisp").Scan(&after); err != nil {
		t.Fatalf("read wisp fence: %v", err)
	}
	if after != before+1 {
		t.Errorf("wisp claim_fence = %d, want %d", after, before+1)
	}

	if err := store.UnclaimIssue(ctx, "fence-wisp", "alice", false); err != nil {
		t.Fatalf("unclaim wisp: %v", err)
	}
	var released int64
	if err := store.db.QueryRowContext(ctx, `SELECT claim_fence FROM wisps WHERE id = ?`, "fence-wisp").Scan(&released); err != nil {
		t.Fatalf("read wisp fence: %v", err)
	}
	if released != after+1 {
		t.Errorf("wisp unclaim claim_fence = %d, want %d", released, after+1)
	}
}

// TestUpsertFenceDiscipline: the import/upsert path bumps the fence exactly
// when the stored assignee changes — with ” and NULL treated as the same
// unassigned state (unclaim writes ”, NullString-bound imports write NULL) so
// content-only re-imports of unassigned rows never bump.
func TestUpsertFenceDiscipline(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "fence-upsert", "alice", time.Hour)
	claimed := readFenceState(t, ctx, store, "fence-upsert")

	upsert := func(assignee string, updatedAt time.Time, rejectStale bool) {
		t.Helper()
		snapshot := &types.Issue{
			ID:        "fence-upsert",
			Title:     "fence fence-upsert",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeTask,
			Assignee:  assignee,
			CreatedAt: time.Now().UTC().Add(-time.Hour),
			UpdatedAt: updatedAt,
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if rejectStale {
			if _, _, err := issueops.InsertIssueIfNew(ctx, tx, "issues", snapshot, storage.BatchCreateOptions{RejectStaleUpserts: true}); err != nil {
				t.Fatalf("stale-guarded upsert: %v", err)
			}
		} else if err := issueops.InsertIssueIntoTable(ctx, tx, "issues", snapshot); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	// Same-assignee re-import (newer timestamp): content only, no bump.
	upsert("alice", time.Now().UTC().Add(time.Minute), false)
	same := readFenceState(t, ctx, store, "fence-upsert")
	if same.fence != claimed.fence {
		t.Errorf("same-assignee upsert moved claim_fence %d → %d, want unchanged", claimed.fence, same.fence)
	}

	// Assignee-change import: ownership transition, bump + row_lock rewrite.
	upsert("bob", time.Now().UTC().Add(2*time.Minute), false)
	assertFenceBumped(t, same, readFenceState(t, ctx, store, "fence-upsert"), "assignee-change upsert")

	// Stale-guarded branch: an OLDER incoming row does not change the assignee
	// and must not bump; a NEWER one with a changed assignee must.
	afterBob := readFenceState(t, ctx, store, "fence-upsert")
	upsert("carol", time.Now().UTC().Add(-2*time.Hour), true)
	stale := readFenceState(t, ctx, store, "fence-upsert")
	if stale.fence != afterBob.fence {
		t.Errorf("stale upsert moved claim_fence %d → %d, want unchanged", afterBob.fence, stale.fence)
	}
	upsert("carol", time.Now().UTC().Add(3*time.Minute), true)
	assertFenceBumped(t, stale, readFenceState(t, ctx, store, "fence-upsert"), "stale-guarded assignee-change upsert")

	// ''/NULL normalization: release (stored assignee becomes ''), then
	// re-import the unassigned row (binds NULL). Both mean unassigned — a
	// content-only sync must not bump.
	if err := store.UnclaimIssue(ctx, "fence-upsert", "carol", false); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	released := readFenceState(t, ctx, store, "fence-upsert")
	upsertUnassigned := func() {
		snapshot := &types.Issue{
			ID:        "fence-upsert",
			Title:     "fence fence-upsert",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now().UTC().Add(-time.Hour),
			UpdatedAt: time.Now().UTC().Add(4 * time.Minute),
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := issueops.InsertIssueIntoTable(ctx, tx, "issues", snapshot); err != nil {
			t.Fatalf("unassigned upsert: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	upsertUnassigned()
	after := readFenceState(t, ctx, store, "fence-upsert")
	if after.fence != released.fence {
		t.Errorf("unassigned re-import moved claim_fence %d → %d — ''/NULL must normalize to the same unassigned state", released.fence, after.fence)
	}
}

// TestFenceSurvivesTableMove: promote/demote rebuild the row in the other
// table; the fence must carry, not reset to the column default — a fence
// moving backward re-validates retired guard snapshots, the one unsound
// direction.
func TestFenceSurvivesTableMove(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Wisp → issues (promote).
	wisp := &types.Issue{
		ID:        "fence-promote",
		Title:     "fence promote",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "seeder"); err != nil {
		t.Fatalf("seed wisp: %v", err)
	}
	if err := store.ClaimIssue(ctx, "fence-promote", "alice"); err != nil {
		t.Fatalf("claim wisp: %v", err)
	}
	var wispFence int64
	if err := store.db.QueryRowContext(ctx, `SELECT claim_fence FROM wisps WHERE id = ?`, "fence-promote").Scan(&wispFence); err != nil {
		t.Fatalf("read wisp fence: %v", err)
	}
	if wispFence == 0 {
		t.Fatal("claimed wisp fence = 0, want bumped before the move")
	}
	if err := store.PromoteFromEphemeral(ctx, "fence-promote", "admin"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	promoted := readFenceState(t, ctx, store, "fence-promote")
	if promoted.fence != wispFence {
		t.Errorf("promote reset claim_fence %d → %d, want carried", wispFence, promoted.fence)
	}

	// Issues → wisps (demote).
	seedClaimedIssue(t, ctx, store, "fence-demote", "alice", time.Hour)
	before := readFenceState(t, ctx, store, "fence-demote")
	if err := store.DemoteToWisp(ctx, "fence-demote", map[string]interface{}{"wisp": true}, "admin"); err != nil {
		t.Fatalf("demote: %v", err)
	}
	var demoted int64
	if err := store.db.QueryRowContext(ctx, `SELECT claim_fence FROM wisps WHERE id = ?`, "fence-demote").Scan(&demoted); err != nil {
		t.Fatalf("read demoted fence: %v", err)
	}
	if demoted != before.fence {
		t.Errorf("demote reset claim_fence %d → %d, want carried", before.fence, demoted)
	}
}
