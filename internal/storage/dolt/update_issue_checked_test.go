package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueCheckedVersionCAS exercises the optional ExpectedVersion
// optimistic-concurrency check layered onto the generic update. The version read
// and the update share ONE transaction, so a stale version is refused with
// storage.ErrVersionMismatch and — critically — the transaction rolls back
// leaving the field UNCHANGED with no `updated` event recorded (the atomic-
// refuse property, mirroring CloseIssueChecked). A nil ExpectedVersion disables
// the check, so behavior is byte-identical to UpdateIssue.
func TestUpdateIssueCheckedVersionCAS(t *testing.T) {
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
	rowVersion := func(t *testing.T, id string) int64 {
		t.Helper()
		return get(t, id).RowVersion
	}
	title := func(t *testing.T, id string) string {
		t.Helper()
		return get(t, id).Title
	}
	countUpdatedEvents := func(t *testing.T, id string) int {
		t.Helper()
		events, err := store.GetEvents(ctx, id, 0)
		if err != nil {
			t.Fatalf("GetEvents(%s): %v", id, err)
		}
		n := 0
		for _, e := range events {
			if e.EventType == types.EventUpdated {
				n++
			}
		}
		return n
	}
	ptr := func(v int64) *int64 { return &v }

	t.Run("matching version updates", func(t *testing.T) {
		createPerm(t, ctx, store, "uc-match")
		v := rowVersion(t, "uc-match")
		if err := store.UpdateIssueChecked(ctx, "uc-match",
			map[string]interface{}{"title": "matched"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(v)}); err != nil {
			t.Fatalf("update with matching version err = %v, want nil", err)
		}
		if got := title(t, "uc-match"); got != "matched" {
			t.Fatalf("uc-match title = %q, want %q (update did not apply)", got, "matched")
		}
		if after := rowVersion(t, "uc-match"); after == v {
			t.Fatalf("RowVersion unchanged after a checked update: still %d", v)
		}
	})

	t.Run("stale version refuses atomically", func(t *testing.T) {
		createPerm(t, ctx, store, "uc-stale")
		v := rowVersion(t, "uc-stale")
		before := title(t, "uc-stale")
		eventsBefore := countUpdatedEvents(t, "uc-stale")
		// v+1 is a version the row provably does not hold.
		err := store.UpdateIssueChecked(ctx, "uc-stale",
			map[string]interface{}{"title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(v + 1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		// Atomic refuse: field unchanged and no `updated` event — the CAS aborted
		// the tx before the update ran.
		if got := title(t, "uc-stale"); got != before {
			t.Fatalf("uc-stale title = %q after refused update, want unchanged %q", got, before)
		}
		if n := countUpdatedEvents(t, "uc-stale"); n != eventsBefore {
			t.Fatalf("updated event count for uc-stale = %d, want %d (tx must have rolled back)", n, eventsBefore)
		}
	})

	t.Run("concurrent write invalidates captured version", func(t *testing.T) {
		createPerm(t, ctx, store, "uc-concurrent")
		v1 := rowVersion(t, "uc-concurrent")
		// A mutating write rewrites row_lock, so the captured v1 is now stale.
		if err := store.UpdateIssue(ctx, "uc-concurrent",
			map[string]interface{}{"priority": 1}, "tester"); err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}
		if v2 := rowVersion(t, "uc-concurrent"); v2 == v1 {
			t.Fatalf("RowVersion unchanged after UpdateIssue (%d); CAS could not detect the write", v2)
		}
		before := title(t, "uc-concurrent")
		err := store.UpdateIssueChecked(ctx, "uc-concurrent",
			map[string]interface{}{"title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(v1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		if got := title(t, "uc-concurrent"); got != before {
			t.Fatalf("uc-concurrent title = %q after stale update, want unchanged %q", got, before)
		}
	})

	t.Run("nil ExpectedVersion is unchanged behavior", func(t *testing.T) {
		createPerm(t, ctx, store, "uc-nil")
		if err := store.UpdateIssueChecked(ctx, "uc-nil",
			map[string]interface{}{"title": "nil-check"}, "tester",
			storage.UpdateIssueOptions{}); err != nil {
			t.Fatalf("nil ExpectedVersion update err = %v, want nil (back-compat)", err)
		}
		if got := title(t, "uc-nil"); got != "nil-check" {
			t.Fatalf("uc-nil title = %q, want %q (update did not apply)", got, "nil-check")
		}
	})

	t.Run("missing id returns ErrNotFound", func(t *testing.T) {
		err := store.UpdateIssueChecked(ctx, "uc-missing",
			map[string]interface{}{"title": "x"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(1)})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("err = %v, want errors.Is(_, ErrNotFound) for absent row", err)
		}
	})

	// The wisp subtests exercise CheckVersionInTx's wisp routing (SELECT row_lock
	// FROM wisps) and updateWispChecked's atomic-refuse seam: it uses a bare
	// BeginTx with a deferred Rollback (no withRetryTx, matching the package-wide
	// wisp write pattern), so a stale version must leave the wisp untouched.
	t.Run("wisp matching version updates", func(t *testing.T) {
		createWisp(t, ctx, store, "uc-wisp-match")
		// Reading the version at all requires the wisp route: a read against the
		// issues table for this id would miss, so a successful update here proves
		// CheckVersionInTx routed to the wisps table.
		v := rowVersion(t, "uc-wisp-match")
		if err := store.UpdateIssueChecked(ctx, "uc-wisp-match",
			map[string]interface{}{"title": "wisp-matched"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(v)}); err != nil {
			t.Fatalf("wisp update with matching version err = %v, want nil", err)
		}
		if got := title(t, "uc-wisp-match"); got != "wisp-matched" {
			t.Fatalf("uc-wisp-match title = %q, want %q", got, "wisp-matched")
		}
	})

	t.Run("wisp stale version refuses atomically", func(t *testing.T) {
		createWisp(t, ctx, store, "uc-wisp-stale")
		v := rowVersion(t, "uc-wisp-stale")
		before := title(t, "uc-wisp-stale")
		err := store.UpdateIssueChecked(ctx, "uc-wisp-stale",
			map[string]interface{}{"title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(v + 1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("wisp err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		// updateWispChecked's deferred Rollback must discard the tx: the wisp's
		// field stays unchanged.
		if got := title(t, "uc-wisp-stale"); got != before {
			t.Fatalf("uc-wisp-stale title = %q after refused update, want unchanged %q", got, before)
		}
	})

	// The demote subtests exercise the no_history/wisp branch of UpdateIssueChecked
	// (CheckVersionInTx + demoteToWispInTx in ONE withRetryTx). A future refactor
	// that re-split the check and the demotion into separate transactions would
	// still pass the plain-update subtests above but break these — the seam the
	// demoteToWispInTx extraction exists to keep atomic.
	inIssuesTable := func(t *testing.T, id string) bool {
		t.Helper()
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&count); err != nil {
			t.Fatalf("query issues table for %s: %v", id, err)
		}
		return count > 0
	}

	t.Run("demote matching version migrates and applies the update", func(t *testing.T) {
		createPerm(t, ctx, store, "uc-demote-match")
		v := rowVersion(t, "uc-demote-match")
		// no_history=true is the demote trigger (mirrors demote_to_wisp_test.go);
		// the title change rides along so "update applied" is unambiguous.
		if err := store.UpdateIssueChecked(ctx, "uc-demote-match",
			map[string]interface{}{"no_history": true, "title": "demoted-match"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(v)}); err != nil {
			t.Fatalf("demote update with matching version err = %v, want nil", err)
		}
		if !store.isActiveWisp(ctx, "uc-demote-match") {
			t.Fatalf("uc-demote-match should be in the wisps table after demotion")
		}
		if inIssuesTable(t, "uc-demote-match") {
			t.Fatalf("uc-demote-match still present in the issues table after demotion")
		}
		iss := get(t, "uc-demote-match")
		if iss.Title != "demoted-match" {
			t.Fatalf("uc-demote-match title = %q, want %q (update did not apply)", iss.Title, "demoted-match")
		}
		if !iss.NoHistory {
			t.Fatalf("uc-demote-match NoHistory = false after demotion, want true")
		}
	})

	t.Run("demote stale version refuses atomically", func(t *testing.T) {
		createPerm(t, ctx, store, "uc-demote-stale")
		v := rowVersion(t, "uc-demote-stale")
		before := title(t, "uc-demote-stale")
		err := store.UpdateIssueChecked(ctx, "uc-demote-stale",
			map[string]interface{}{"no_history": true, "title": "should-not-apply"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: ptr(v + 1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		// Atomic refuse: the demotion tx rolled back, so the issue is NOT a wisp,
		// still lives in the issues table, and its field is unchanged.
		if store.isActiveWisp(ctx, "uc-demote-stale") {
			t.Fatalf("uc-demote-stale demoted to a wisp despite stale version; CAS did not abort")
		}
		if !inIssuesTable(t, "uc-demote-stale") {
			t.Fatalf("uc-demote-stale missing from the issues table after refused demote (tx must roll back)")
		}
		if got := title(t, "uc-demote-stale"); got != before {
			t.Fatalf("uc-demote-stale title = %q after refused demote, want unchanged %q", got, before)
		}
	})
}
