package dolt

import (
	"errors"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueCheckedFieldGuards exercises the bd-wsqvw semantic-field
// compare-and-set guards (UpdateIssueOptions.ExpectedAssignee/ExpectedStatus)
// layered onto UpdateIssueChecked: the guard read and the update share ONE
// transaction, so a stale guard refuses with the typed mismatch error and the
// transaction rolls back leaving the row untouched (the atomic-refuse property
// of the version CAS, extended to the coordination fields). The two motivating
// transitions are covered explicitly: reassign X→Y (park) and claim-on-behalf
// with a status guard (restore) — the transitions neither --claim nor
// unclaim --if-assignee can express.
func TestUpdateIssueCheckedFieldGuards(t *testing.T) {
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
	sptr := func(v string) *string { return &v }

	t.Run("reassign X to Y with matching assignee guard", func(t *testing.T) {
		createPerm(t, ctx, store, "ug-park")
		if err := store.ClaimIssue(ctx, "ug-park", "worker"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		// The park transition: hand the bead to the parking assignee only while
		// the reclaimed worker still holds it.
		if err := store.UpdateIssueChecked(ctx, "ug-park",
			map[string]interface{}{"assignee": "mayor"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("worker")}); err != nil {
			t.Fatalf("guarded reassign err = %v, want nil", err)
		}
		if got := get(t, "ug-park").Assignee; got != "mayor" {
			t.Fatalf("assignee = %q after guarded reassign, want %q", got, "mayor")
		}
	})

	t.Run("stale assignee guard refuses atomically", func(t *testing.T) {
		createPerm(t, ctx, store, "ug-stale")
		if err := store.ClaimIssue(ctx, "ug-stale", "thief"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		err := store.UpdateIssueChecked(ctx, "ug-stale",
			map[string]interface{}{"assignee": "mayor", "title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("worker")})
		if !errors.Is(err, storage.ErrAssigneeMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrAssigneeMismatch)", err)
		}
		iss := get(t, "ug-stale")
		if iss.Assignee != "thief" {
			t.Fatalf("assignee = %q after refused reassign, want unchanged %q", iss.Assignee, "thief")
		}
		if iss.Title == "should-not-apply" {
			t.Fatalf("title applied despite refused guard; tx must roll back atomically")
		}
	})

	t.Run("claim-on-behalf with empty assignee and status guards", func(t *testing.T) {
		createPerm(t, ctx, store, "ug-restore")
		// The restore transition: assign to the crew owner and start it, only
		// while still open and unassigned. --if-assignee '' is a real guard.
		if err := store.UpdateIssueChecked(ctx, "ug-restore",
			map[string]interface{}{"assignee": "owner", "status": "in_progress"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr(""), ExpectedStatus: sptr("open")}); err != nil {
			t.Fatalf("claim-on-behalf err = %v, want nil", err)
		}
		iss := get(t, "ug-restore")
		if iss.Assignee != "owner" || iss.Status != types.StatusInProgress {
			t.Fatalf("got assignee=%q status=%q, want owner/in_progress", iss.Assignee, iss.Status)
		}
	})

	t.Run("empty assignee guard refuses when assigned", func(t *testing.T) {
		createPerm(t, ctx, store, "ug-taken")
		if err := store.ClaimIssue(ctx, "ug-taken", "worker"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		err := store.UpdateIssueChecked(ctx, "ug-taken",
			map[string]interface{}{"assignee": "owner"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("")})
		if !errors.Is(err, storage.ErrAssigneeMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrAssigneeMismatch) for assigned row", err)
		}
		if got := get(t, "ug-taken").Assignee; got != "worker" {
			t.Fatalf("assignee = %q, want unchanged %q", got, "worker")
		}
	})

	t.Run("status guard mismatch is typed and atomic", func(t *testing.T) {
		createPerm(t, ctx, store, "ug-status")
		if err := store.ClaimIssue(ctx, "ug-status", "worker"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		// Conjunction: the assignee guard holds but the status guard does not —
		// the whole precondition must fail with the status verdict.
		err := store.UpdateIssueChecked(ctx, "ug-status",
			map[string]interface{}{"assignee": "owner"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("worker"), ExpectedStatus: sptr("open")})
		if !errors.Is(err, storage.ErrStatusMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrStatusMismatch)", err)
		}
		if got := get(t, "ug-status").Assignee; got != "worker" {
			t.Fatalf("assignee = %q after refused update, want unchanged %q", got, "worker")
		}
	})

	t.Run("guards compose with ExpectedVersion", func(t *testing.T) {
		createPerm(t, ctx, store, "ug-both")
		v := get(t, "ug-both").RowVersion
		iptr := func(v int64) *int64 { return &v }
		// Field guards hold, version stale: the version verdict wins.
		err := store.UpdateIssueChecked(ctx, "ug-both",
			map[string]interface{}{"title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: iptr(v + 1), ExpectedAssignee: sptr("")})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		// Both hold: applies.
		if err := store.UpdateIssueChecked(ctx, "ug-both",
			map[string]interface{}{"title": "both-held"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: iptr(v), ExpectedAssignee: sptr("")}); err != nil {
			t.Fatalf("update with both preconditions held err = %v, want nil", err)
		}
		if got := get(t, "ug-both").Title; got != "both-held" {
			t.Fatalf("title = %q, want %q", got, "both-held")
		}
	})

	t.Run("wisp guard routes to wisps table", func(t *testing.T) {
		createWisp(t, ctx, store, "ug-wisp")
		// A matching guard proves CheckExpectedFieldsInTx read the wisps table
		// (an issues-table read for this id would miss and refuse).
		if err := store.UpdateIssueChecked(ctx, "ug-wisp",
			map[string]interface{}{"title": "wisp-guarded"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("")}); err != nil {
			t.Fatalf("guarded wisp update err = %v, want nil", err)
		}
		if got := get(t, "ug-wisp").Title; got != "wisp-guarded" {
			t.Fatalf("wisp title = %q, want %q", got, "wisp-guarded")
		}
		err := store.UpdateIssueChecked(ctx, "ug-wisp",
			map[string]interface{}{"title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("someone")})
		if !errors.Is(err, storage.ErrAssigneeMismatch) {
			t.Fatalf("wisp err = %v, want errors.Is(_, ErrAssigneeMismatch)", err)
		}
	})
}

// TestUpdateIssueCheckedFenceGuard exercises UpdateIssueOptions.ExpectedFence:
// the ownership-generation compare-and-set behind `bd update --if-fence`. It is
// the guard that survives the holder's own content writes (unlike
// ExpectedVersion, whose row_lock moves on every write) and still catches a
// reclaim-and-reissue to the SAME assignee (which --if-assignee cannot see).
func TestUpdateIssueCheckedFenceGuard(t *testing.T) {
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
	sptr := func(v string) *string { return &v }
	iptr := func(v int64) *int64 { return &v }

	t.Run("matching fence applies", func(t *testing.T) {
		createPerm(t, ctx, store, "uf-match")
		if err := store.ClaimIssue(ctx, "uf-match", "worker"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		fence := get(t, "uf-match").ClaimFence
		if err := store.UpdateIssueChecked(ctx, "uf-match",
			map[string]interface{}{"notes": "still mine"}, "worker",
			storage.UpdateIssueOptions{ExpectedFence: iptr(fence)}); err != nil {
			t.Fatalf("guarded update with live fence err = %v, want nil", err)
		}
		if got := get(t, "uf-match").Notes; got != "still mine" {
			t.Fatalf("notes = %q, want %q", got, "still mine")
		}
	})

	t.Run("fence 0 is a real never-claimed assertion", func(t *testing.T) {
		createPerm(t, ctx, store, "uf-zero")
		if err := store.UpdateIssueChecked(ctx, "uf-zero",
			map[string]interface{}{"notes": "pristine"}, "tester",
			storage.UpdateIssueOptions{ExpectedFence: iptr(0)}); err != nil {
			t.Fatalf("fence-0 guard on a never-claimed row err = %v, want nil", err)
		}
		if err := store.ClaimIssue(ctx, "uf-zero", "worker"); err != nil {
			t.Fatalf("claim: %v", err)
		}
		err := store.UpdateIssueChecked(ctx, "uf-zero",
			map[string]interface{}{"notes": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedFence: iptr(0)})
		if !errors.Is(err, storage.ErrFenceMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrFenceMismatch) once the row has been claimed", err)
		}
		if got := get(t, "uf-zero").Notes; got != "pristine" {
			t.Fatalf("notes = %q after refused update, want unchanged %q", got, "pristine")
		}
	})

	t.Run("stale fence refuses atomically", func(t *testing.T) {
		createPerm(t, ctx, store, "uf-stale")
		if err := store.ClaimIssue(ctx, "uf-stale", "worker"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		stale := get(t, "uf-stale").ClaimFence
		// Ownership moves on: release + re-claim by the SAME actor. The assignee
		// is identical afterwards, so only the fence can catch this.
		if err := store.UnclaimIssue(ctx, "uf-stale", "worker", false, nil); err != nil {
			t.Fatalf("unclaim: %v", err)
		}
		if err := store.ClaimIssue(ctx, "uf-stale", "worker"); err != nil {
			t.Fatalf("re-claim: %v", err)
		}
		err := store.UpdateIssueChecked(ctx, "uf-stale",
			map[string]interface{}{"notes": "should-not-apply", "priority": 1}, "worker",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("worker"), ExpectedFence: iptr(stale)})
		if !errors.Is(err, storage.ErrFenceMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrFenceMismatch)", err)
		}
		iss := get(t, "uf-stale")
		if iss.Notes == "should-not-apply" || iss.Priority == 1 {
			t.Fatalf("refused update partially applied (notes=%q priority=%d); the tx must roll back", iss.Notes, iss.Priority)
		}
	})

	t.Run("content writes do not invalidate the fence", func(t *testing.T) {
		createPerm(t, ctx, store, "uf-content")
		if err := store.ClaimIssue(ctx, "uf-content", "worker"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		fence := get(t, "uf-content").ClaimFence
		version := get(t, "uf-content").RowVersion
		if err := store.UpdateIssue(ctx, "uf-content",
			map[string]interface{}{"title": "edited"}, "worker"); err != nil {
			t.Fatalf("content update: %v", err)
		}
		if got := get(t, "uf-content").RowVersion; got == version {
			t.Fatalf("RowVersion unchanged after a content write; the premise of this test is wrong")
		}
		if err := store.UpdateIssueChecked(ctx, "uf-content",
			map[string]interface{}{"notes": "second write"}, "worker",
			storage.UpdateIssueOptions{ExpectedFence: iptr(fence)}); err != nil {
			t.Fatalf("fence guard err = %v after a content-only write, want nil (fence tracks ownership, not writes)", err)
		}
	})

	t.Run("fence composes with assignee, status and version guards", func(t *testing.T) {
		createPerm(t, ctx, store, "uf-compose")
		if err := store.ClaimIssue(ctx, "uf-compose", "worker"); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		iss := get(t, "uf-compose")
		// Every guard holds: the update applies.
		if err := store.UpdateIssueChecked(ctx, "uf-compose",
			map[string]interface{}{"notes": "all-held"}, "worker",
			storage.UpdateIssueOptions{
				ExpectedVersion:  iptr(iss.RowVersion),
				ExpectedAssignee: sptr("worker"),
				ExpectedStatus:   sptr(string(types.StatusInProgress)),
				ExpectedFence:    iptr(iss.ClaimFence),
			}); err != nil {
			t.Fatalf("all four guards held but err = %v, want nil", err)
		}
		if got := get(t, "uf-compose").Notes; got != "all-held" {
			t.Fatalf("notes = %q, want %q", got, "all-held")
		}
		// Conjunction: assignee and status still hold, only the fence is stale.
		cur := get(t, "uf-compose")
		err := store.UpdateIssueChecked(ctx, "uf-compose",
			map[string]interface{}{"notes": "should-not-apply"}, "worker",
			storage.UpdateIssueOptions{
				ExpectedAssignee: sptr("worker"),
				ExpectedStatus:   sptr(string(types.StatusInProgress)),
				ExpectedFence:    iptr(cur.ClaimFence + 1),
			})
		if !errors.Is(err, storage.ErrFenceMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrFenceMismatch) when only the fence is stale", err)
		}
		if got := get(t, "uf-compose").Notes; got != "all-held" {
			t.Fatalf("notes = %q after refused update, want unchanged", got)
		}
	})

	t.Run("wisp fence guard routes to wisps table", func(t *testing.T) {
		createWisp(t, ctx, store, "uf-wisp")
		// A wisp starts unclaimed at fence 0; reading it at all requires the
		// wisps route (an issues-table read for this id would miss and refuse).
		if err := store.UpdateIssueChecked(ctx, "uf-wisp",
			map[string]interface{}{"title": "wisp-fenced"}, "tester",
			storage.UpdateIssueOptions{ExpectedFence: iptr(0)}); err != nil {
			t.Fatalf("guarded wisp update err = %v, want nil", err)
		}
		err := store.UpdateIssueChecked(ctx, "uf-wisp",
			map[string]interface{}{"title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedFence: iptr(9)})
		if !errors.Is(err, storage.ErrFenceMismatch) {
			t.Fatalf("wisp err = %v, want errors.Is(_, ErrFenceMismatch)", err)
		}
		if got := get(t, "uf-wisp").Title; got != "wisp-fenced" {
			t.Fatalf("wisp title = %q after refused update, want unchanged", got)
		}
	})
}

// TestGuardedReassignConcurrent races two guarded reassigns of the same row —
// both conditioned on the same expected assignee — and requires exactly one
// winner: the loser must observe the winner's write (via the shared-tx guard
// re-check after withRetryTx replays the commit-time row_lock collision) and
// refuse with ErrAssigneeMismatch, never silently clobber. This is the CAS
// property the blind `bd update -a` check-then-act pattern lacks.
func TestGuardedReassignConcurrent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "ug-race")
	if err := store.ClaimIssue(ctx, "ug-race", "worker"); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	expected := "worker"
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, newAssignee := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(i int, newAssignee string) {
			defer wg.Done()
			errs[i] = store.UpdateIssueChecked(ctx, "ug-race",
				map[string]interface{}{"assignee": newAssignee}, newAssignee,
				storage.UpdateIssueOptions{ExpectedAssignee: &expected})
		}(i, newAssignee)
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, storage.ErrAssigneeMismatch):
			// the loser's verdict
		default:
			t.Fatalf("racer %d unexpected error: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1 (errs: %v)", winners, errs)
	}

	iss, err := store.GetIssue(ctx, "ug-race")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.Assignee != "alpha" && iss.Assignee != "beta" {
		t.Fatalf("final assignee = %q, want the winner's value", iss.Assignee)
	}
}
