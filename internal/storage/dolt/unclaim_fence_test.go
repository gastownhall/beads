package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestUnclaimFenceGuard covers the ownership-fence conjunct on both release
// paths (`bd unclaim --if-fence`). The security rule under test: a guard
// establishes FRESHNESS, not AUTHORITY. Passing the right fence never lets a
// non-owner release someone else's claim, and --force — which waives the owner
// check — never waives a fence the caller supplied.
func TestUnclaimFenceGuard(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	get := func(t *testing.T, id string) *types.Issue {
		t.Helper()
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if iss == nil {
			t.Fatalf("GetIssue(%s) returned nil issue", id)
		}
		return iss
	}
	iptr := func(v int64) *int64 { return &v }
	claimed := func(t *testing.T, id, actor string) int64 {
		t.Helper()
		createPerm(t, ctx, store, id)
		if err := store.ClaimIssue(ctx, id, actor); err != nil {
			t.Fatalf("seed claim %s: %v", id, err)
		}
		return get(t, id).ClaimFence
	}

	t.Run("owner with the live fence releases", func(t *testing.T) {
		fence := claimed(t, "uf-rel-ok", "alice")
		if err := store.UnclaimIssue(ctx, "uf-rel-ok", "alice", false, iptr(fence)); err != nil {
			t.Fatalf("guarded release by the holder err = %v, want nil", err)
		}
		iss := get(t, "uf-rel-ok")
		if iss.Assignee != "" || iss.Status != types.StatusOpen {
			t.Fatalf("after release: assignee=%q status=%q, want unassigned/open", iss.Assignee, iss.Status)
		}
		if iss.ClaimFence != fence+1 {
			t.Fatalf("claim_fence = %d after release, want %d", iss.ClaimFence, fence+1)
		}
	})

	t.Run("owner with a stale fence is refused", func(t *testing.T) {
		fence := claimed(t, "uf-rel-stale", "alice")
		// The claim is released and re-taken by the same actor: same holder,
		// new ownership generation.
		if err := store.UnclaimIssue(ctx, "uf-rel-stale", "alice", false, nil); err != nil {
			t.Fatalf("unclaim: %v", err)
		}
		if err := store.ClaimIssue(ctx, "uf-rel-stale", "alice"); err != nil {
			t.Fatalf("re-claim: %v", err)
		}
		err := store.UnclaimIssue(ctx, "uf-rel-stale", "alice", false, iptr(fence))
		if !errors.Is(err, storage.ErrFenceMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrFenceMismatch)", err)
		}
		iss := get(t, "uf-rel-stale")
		if iss.Assignee != "alice" || iss.Status != types.StatusInProgress {
			t.Fatalf("stale guarded release disturbed the live claim: assignee=%q status=%q", iss.Assignee, iss.Status)
		}
	})

	// The P0 rule: a fence NEVER authorizes a cross-actor release. A stranger
	// quoting the correct, live fence still gets ErrNotOwner — the owner check
	// runs first and is unchanged.
	t.Run("correct fence does not authorize a cross-actor release", func(t *testing.T) {
		fence := claimed(t, "uf-rel-cross", "alice")
		err := store.UnclaimIssue(ctx, "uf-rel-cross", "controller", false, iptr(fence))
		if !errors.Is(err, storage.ErrNotOwner) {
			t.Fatalf("err = %v, want errors.Is(_, ErrNotOwner) — a guard is not a credential", err)
		}
		iss := get(t, "uf-rel-cross")
		if iss.Assignee != "alice" || iss.ClaimFence != fence {
			t.Fatalf("cross-actor release landed: assignee=%q fence=%d", iss.Assignee, iss.ClaimFence)
		}
	})

	t.Run("force does not skip a supplied fence", func(t *testing.T) {
		fence := claimed(t, "uf-rel-force", "alice")
		err := store.UnclaimIssue(ctx, "uf-rel-force", "admin", true, iptr(fence+7))
		if !errors.Is(err, storage.ErrFenceMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrFenceMismatch) under --force", err)
		}
		iss := get(t, "uf-rel-force")
		if iss.Assignee != "alice" || iss.Status != types.StatusInProgress {
			t.Fatalf("force bypassed a failing guard: assignee=%q status=%q", iss.Assignee, iss.Status)
		}
		// With the live fence, force still does what it always did.
		if err := store.UnclaimIssue(ctx, "uf-rel-force", "admin", true, iptr(fence)); err != nil {
			t.Fatalf("forced release with the live fence err = %v, want nil", err)
		}
		if got := get(t, "uf-rel-force").Assignee; got != "" {
			t.Fatalf("assignee = %q after forced release, want empty", got)
		}
	})

	t.Run("if-assignee CAS composes with the fence", func(t *testing.T) {
		fence := claimed(t, "uf-cas-ok", "alice")
		if err := store.UnclaimIssueIfAssignee(ctx, "uf-cas-ok", "supervisor", "alice", iptr(fence)); err != nil {
			t.Fatalf("conditional release with both guards held err = %v, want nil", err)
		}
		if got := get(t, "uf-cas-ok").Assignee; got != "" {
			t.Fatalf("assignee = %q after conditional release, want empty", got)
		}
	})

	t.Run("if-assignee CAS reports a fence-only miss as a fence mismatch", func(t *testing.T) {
		fence := claimed(t, "uf-cas-fence", "alice")
		if err := store.UnclaimIssue(ctx, "uf-cas-fence", "alice", false, nil); err != nil {
			t.Fatalf("unclaim: %v", err)
		}
		if err := store.ClaimIssue(ctx, "uf-cas-fence", "alice"); err != nil {
			t.Fatalf("re-claim: %v", err)
		}
		// The assignee guard holds (still alice) — only the fence is stale, so
		// the verdict must name the fence rather than the holder.
		err := store.UnclaimIssueIfAssignee(ctx, "uf-cas-fence", "supervisor", "alice", iptr(fence))
		if !errors.Is(err, storage.ErrFenceMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrFenceMismatch)", err)
		}
		if errors.Is(err, storage.ErrAssigneeMismatch) {
			t.Fatalf("fence-only miss reported as an assignee mismatch: %v", err)
		}
		if got := get(t, "uf-cas-fence").Assignee; got != "alice" {
			t.Fatalf("assignee = %q after refused conditional release, want alice", got)
		}
	})

	t.Run("if-assignee CAS still reports a holder change as an assignee mismatch", func(t *testing.T) {
		fence := claimed(t, "uf-cas-holder", "alice")
		if err := store.UnclaimIssue(ctx, "uf-cas-holder", "alice", false, nil); err != nil {
			t.Fatalf("unclaim: %v", err)
		}
		if err := store.ClaimIssue(ctx, "uf-cas-holder", "bob"); err != nil {
			t.Fatalf("re-claim by bob: %v", err)
		}
		err := store.UnclaimIssueIfAssignee(ctx, "uf-cas-holder", "supervisor", "alice", iptr(fence))
		if !errors.Is(err, storage.ErrAssigneeMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrAssigneeMismatch) when the holder moved", err)
		}
		if got := get(t, "uf-cas-holder").Assignee; got != "bob" {
			t.Fatalf("bob's claim was disturbed: assignee = %q", got)
		}
	})
}
