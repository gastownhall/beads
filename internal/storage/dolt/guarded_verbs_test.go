package dolt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func guardCtx(ctx context.Context, assignee string, fence int64) context.Context {
	g := issueops.Guard{}
	if assignee != "" {
		g.Assignee = &assignee
	}
	if fence >= 0 {
		g.Fence = &fence
	}
	return issueops.WithGuard(ctx, g)
}

func requirePrecondition(t *testing.T, err error, what string) *storage.PreconditionFailedError {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want PreconditionFailedError, got nil", what)
	}
	var pf *storage.PreconditionFailedError
	if !errors.As(err, &pf) {
		t.Fatalf("%s: want PreconditionFailedError, got %v", what, err)
	}
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Errorf("%s: error does not unwrap to ErrPreconditionFailed", what)
	}
	return pf
}

// TestGuardedUnclaimReleasesCrossActor: class-T authorization — a transition
// verb carrying a satisfied explicit guard is authorized regardless of caller
// actor (the guard IS the credential); the fence bumps as usual.
func TestGuardedUnclaimReleasesCrossActor(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-release", "alice", time.Hour)
	snap := readFenceState(t, ctx, store, "guard-release")

	// "controller" does not own the claim and does not pass force — the
	// satisfied guard authorizes the release.
	gctx := guardCtx(ctx, "alice", snap.fence)
	if err := store.UnclaimIssue(gctx, "guard-release", "controller", false); err != nil {
		t.Fatalf("guarded cross-actor unclaim: %v", err)
	}
	ls := readLeaseState(t, ctx, store, "guard-release")
	if ls.status != "open" || ls.assignee.String != "" {
		t.Errorf("after guarded release: status=%q assignee=%q, want open/unassigned", ls.status, ls.assignee.String)
	}
	assertFenceBumped(t, snap, readFenceState(t, ctx, store, "guard-release"), "guarded unclaim")
}

// TestGuardedUnclaimConflictsOnStaleFence: a release quoting a superseded
// ownership snapshot must fail typed and leave the row untouched — this is
// the P1 stomp-window closure.
func TestGuardedUnclaimConflictsOnStaleFence(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-stale", "alice", time.Hour)
	stale := readFenceState(t, ctx, store, "guard-stale")

	// Ownership moves twice after the snapshot: release + fresh claim by bob.
	if err := store.UnclaimIssue(ctx, "guard-stale", "alice", false); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	if err := store.ClaimIssue(ctx, "guard-stale", "bob"); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	current := readFenceState(t, ctx, store, "guard-stale")

	gctx := guardCtx(ctx, "alice", stale.fence)
	err := store.UnclaimIssue(gctx, "guard-stale", "controller", false)
	pf := requirePrecondition(t, err, "stale-fence unclaim")
	if pf.CurrentAssignee != "bob" || pf.CurrentFence != current.fence {
		t.Errorf("conflict carries current=(%q,%d), want (bob,%d)", pf.CurrentAssignee, pf.CurrentFence, current.fence)
	}

	ls := readLeaseState(t, ctx, store, "guard-stale")
	if ls.status != "in_progress" || ls.assignee.String != "bob" {
		t.Errorf("bob's fresh claim was disturbed: status=%q assignee=%q", ls.status, ls.assignee.String)
	}
}

// TestForceNeverSkipsGuards: --force bypasses the owner check only; a
// supplied guard is still evaluated.
func TestForceNeverSkipsGuards(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-force", "alice", time.Hour)
	snap := readFenceState(t, ctx, store, "guard-force")

	gctx := guardCtx(ctx, "alice", snap.fence+7) // wrong fence
	err := store.UnclaimIssue(gctx, "guard-force", "admin", true)
	requirePrecondition(t, err, "force with failing guard")

	ls := readLeaseState(t, ctx, store, "guard-force")
	if ls.status != "in_progress" || ls.assignee.String != "alice" {
		t.Errorf("force bypassed a failing guard: status=%q assignee=%q", ls.status, ls.assignee.String)
	}
}

// TestGuardedCloseRejectsZombie: a holder whose claim was reclaimed and
// re-claimed cannot complete the bead with its stale snapshot — the P2
// zombie-close closure for guarded completion.
func TestGuardedCloseRejectsZombie(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-zombie", "alice", time.Hour)
	zombieSnap := readFenceState(t, ctx, store, "guard-zombie")

	if err := store.UnclaimIssue(ctx, "guard-zombie", "alice", false); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	if err := store.ClaimIssue(ctx, "guard-zombie", "alice2"); err != nil {
		t.Fatalf("re-claim: %v", err)
	}

	gctx := guardCtx(ctx, "alice", zombieSnap.fence)
	err := store.CloseIssue(gctx, "guard-zombie", "zombie completing stale work", "alice", "")
	requirePrecondition(t, err, "zombie close")

	ls := readLeaseState(t, ctx, store, "guard-zombie")
	if ls.status != "in_progress" || ls.assignee.String != "alice2" {
		t.Errorf("zombie close landed: status=%q assignee=%q", ls.status, ls.assignee.String)
	}
}

// TestGuardedCloseSucceedsForCurrentHolder: the happy path — a close guarded
// on the live ownership snapshot completes.
func TestGuardedCloseSucceedsForCurrentHolder(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-close-ok", "alice", time.Hour)
	snap := readFenceState(t, ctx, store, "guard-close-ok")

	gctx := guardCtx(ctx, "alice", snap.fence)
	if err := store.CloseIssue(gctx, "guard-close-ok", "done", "alice", ""); err != nil {
		t.Fatalf("guarded close: %v", err)
	}
	ls := readLeaseState(t, ctx, store, "guard-close-ok")
	if ls.status != "closed" {
		t.Errorf("status = %q, want closed", ls.status)
	}
}

// TestGuardedUpdateAssigneeGuard: in-place mutations honor guards too — a
// metadata/content write guarded on a stale owner fails typed; guarded on the
// live owner it lands.
func TestGuardedUpdateAssigneeGuard(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-update", "alice", time.Hour)

	wrong := "bob"
	gctx := issueops.WithGuard(ctx, issueops.Guard{Assignee: &wrong})
	err := store.UpdateIssue(gctx, "guard-update", map[string]interface{}{"notes": "stale writer"}, "bob")
	requirePrecondition(t, err, "mismatched-assignee update")

	right := "alice"
	gctx = issueops.WithGuard(ctx, issueops.Guard{Assignee: &right})
	if err := store.UpdateIssue(gctx, "guard-update", map[string]interface{}{"notes": "current holder"}, "alice"); err != nil {
		t.Fatalf("guarded update by holder: %v", err)
	}
	got, err := store.GetIssue(ctx, "guard-update")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Notes != "current holder" {
		t.Errorf("notes = %q, want %q", got.Notes, "current holder")
	}
}

// TestGuardedWispVerbs: guards are tier-complete — a wisp-table row honors
// the same guard semantics.
func TestGuardedWispVerbs(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	wisp := &types.Issue{
		ID:        "guard-wisp",
		Title:     "guard wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "seeder"); err != nil {
		t.Fatalf("seed wisp: %v", err)
	}
	if err := store.ClaimIssue(ctx, "guard-wisp", "alice"); err != nil {
		t.Fatalf("claim wisp: %v", err)
	}
	var fence int64
	if err := store.db.QueryRowContext(ctx, `SELECT claim_fence FROM wisps WHERE id = ?`, "guard-wisp").Scan(&fence); err != nil {
		t.Fatalf("read wisp fence: %v", err)
	}

	// Stale guard fails.
	gctx := guardCtx(ctx, "alice", fence+5)
	requirePrecondition(t, store.UnclaimIssue(gctx, "guard-wisp", "controller", false), "stale wisp unclaim")

	// Current guard releases.
	gctx = guardCtx(ctx, "alice", fence)
	if err := store.UnclaimIssue(gctx, "guard-wisp", "controller", false); err != nil {
		t.Fatalf("guarded wisp unclaim: %v", err)
	}
}

// TestGuardedCloseAlreadyClosedSemantics: idempotency must not bypass the
// guard — a same-guard retry after one's own close stays AlreadyClosed
// success, while a stale snapshot on an already-closed row conflicts.
func TestGuardedCloseAlreadyClosedSemantics(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-idem-close", "alice", time.Hour)
	snap := readFenceState(t, ctx, store, "guard-idem-close")

	gctx := guardCtx(ctx, "alice", snap.fence)
	if err := store.CloseIssue(gctx, "guard-idem-close", "done", "alice", ""); err != nil {
		t.Fatalf("guarded close: %v", err)
	}
	// Same-guard retry: close neither clears assignee nor bumps the fence,
	// so the holder's retry still matches and stays idempotent.
	if err := store.CloseIssue(gctx, "guard-idem-close", "done again", "alice", ""); err != nil {
		t.Fatalf("same-guard retry should be AlreadyClosed success, got: %v", err)
	}
	// Stale snapshot (wrong fence) on the closed row: typed conflict, not a
	// false completion signal.
	staleCtx := guardCtx(ctx, "alice", snap.fence+9)
	requirePrecondition(t, store.CloseIssue(staleCtx, "guard-idem-close", "zombie", "alice", ""), "stale-guard close of closed row")
}

// TestGuardedUnclaimOfReleasedRowConflicts: the commonest orchestrator race —
// the row was already released — maps to the typed conflict, not a generic
// error, so retry logic keys on one contract for every "state moved" outcome.
func TestGuardedUnclaimOfReleasedRowConflicts(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "guard-released", "alice", time.Hour)
	snap := readFenceState(t, ctx, store, "guard-released")
	if err := store.UnclaimIssue(ctx, "guard-released", "alice", false); err != nil {
		t.Fatalf("release: %v", err)
	}

	gctx := guardCtx(ctx, "alice", snap.fence)
	pf := requirePrecondition(t, store.UnclaimIssue(gctx, "guard-released", "controller", false), "guarded unclaim of released row")
	if pf.CurrentAssignee != "" {
		t.Errorf("conflict current_assignee = %q, want unassigned", pf.CurrentAssignee)
	}
}
