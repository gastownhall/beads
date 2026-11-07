//go:build cgo

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// blockedStatusHold sets the row's status to 'blocked' — the hold a hand or a
// tracker import writes — leaving is_blocked whatever the dependency graph
// says.
func blockedStatusHold(t *testing.T, store *dolt.DoltStore, id string) {
	t.Helper()
	if err := store.UpdateIssue(context.Background(), id, map[string]interface{}{"status": string(types.StatusBlocked)}, "test"); err != nil {
		t.Fatalf("set %s status blocked: %v", id, err)
	}
}

// storedIsBlocked reads the row's denormalized flag, the column the check
// reports beside the status.
func storedIsBlocked(t *testing.T, store *dolt.DoltStore, id string) bool {
	t.Helper()
	var flag bool
	if err := store.UnderlyingDB().QueryRowContext(context.Background(),
		"SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&flag); err != nil {
		t.Fatalf("read is_blocked(%s): %v", id, err)
	}
	return flag
}

// requireStoredIsBlocked pins a fixture's stored flag before the check runs,
// so a case that means to be consistent (or drifted) fails on its setup, not
// on the check under test.
func requireStoredIsBlocked(t *testing.T, store *dolt.DoltStore, id string, want bool) {
	t.Helper()
	if got := storedIsBlocked(t, store, id); got != want {
		t.Fatalf("precondition: %s stored is_blocked = %v, want %v", id, got, want)
	}
}

// errorSearcher is a BlockedStatusSearcher whose read always fails, standing
// in for a database the check cannot query.
type errorSearcher struct{ err error }

func (e errorSearcher) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, e.err
}

// TestCheckBlockedStatus pins the Blocked Status advisory: a status='blocked'
// row whose stored dependency-blocked flag is clear is reported for review
// with count, bounded deterministic examples and manual guidance, a
// consistent database (graph-held holds included) is OK, and no status or
// flag is ever changed by the check itself.
func TestCheckBlockedStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("hold_with_no_blocker_ever_recorded_is_reported", func(t *testing.T) {
		store := newTestDoltStore(t, "bstat")
		if err := store.CreateIssue(ctx, newTestIssue("bstat-lone"), "test"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		blockedStatusHold(t, store, "bstat-lone")
		requireStoredIsBlocked(t, store, "bstat-lone", false)

		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusWarning {
			t.Fatalf("Status = %q (%s), want %q", check.Status, check.Message, StatusWarning)
		}
		if want := "1 issue(s) remain in status 'blocked' while their stored dependency-blocked flag is clear"; check.Message != want {
			t.Errorf("Message = %q, want %q", check.Message, want)
		}
		if check.Detail != "bstat-lone" {
			t.Errorf("Detail = %q, want the held id", check.Detail)
		}
		if check.Fix == "" || !strings.Contains(check.Fix, "bd update <id> --status open") {
			t.Errorf("Fix must name the reviewed release, got %q", check.Fix)
		}
		// The adapter the full doctor uses reports the same thing.
		if adapted := CheckBlockedStatusWithStore(&SharedStore{store: store}); adapted.Status != StatusWarning || adapted.Message != check.Message {
			t.Errorf("CheckBlockedStatusWithStore = %q (%s), want the same warning", adapted.Status, adapted.Message)
		}
		// Advisory only: nothing was mutated.
		issue, err := store.GetIssue(ctx, "bstat-lone")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if issue.Status != types.StatusBlocked {
			t.Errorf("status = %q after the check, want 'blocked' untouched", issue.Status)
		}
	})

	t.Run("hold_that_outlived_its_last_blocker_is_reported", func(t *testing.T) {
		store := newTestDoltStore(t, "bstat")
		for _, id := range []string{"bstat-blocker", "bstat-held"} {
			if err := store.CreateIssue(ctx, newTestIssue(id), "test"); err != nil {
				t.Fatalf("CreateIssue(%s): %v", id, err)
			}
		}
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bstat-held", DependsOnID: "bstat-blocker", Type: types.DepBlocks}, "test"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		blockedStatusHold(t, store, "bstat-held")
		requireStoredIsBlocked(t, store, "bstat-held", true)

		if err := store.CloseIssue(ctx, "bstat-blocker", "done", "test", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		requireStoredIsBlocked(t, store, "bstat-held", false)

		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusWarning || !strings.Contains(check.Detail, "bstat-held") {
			t.Fatalf("check = %q (%s / %s), want a warning naming bstat-held", check.Status, check.Message, check.Detail)
		}
	})

	t.Run("consistent_database_is_ok_and_changes_nothing", func(t *testing.T) {
		store := newTestDoltStore(t, "bstat")
		rows := []string{
			"bstat-plain", "bstat-closed", "bstat-pinned",
			"bstat-blocker",
			"bstat-held-blocks", "bstat-held-conditional",
			"bstat-parent-blocker", "bstat-child",
			"bstat-gate", "bstat-gate-child", "bstat-held-waits",
		}
		for _, id := range rows {
			if err := store.CreateIssue(ctx, newTestIssue(id), "test"); err != nil {
				t.Fatalf("CreateIssue(%s): %v", id, err)
			}
		}
		if err := store.CloseIssue(ctx, "bstat-closed", "done", "test", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		if err := store.UpdateIssue(ctx, "bstat-pinned", map[string]interface{}{"status": string(types.StatusPinned)}, "test"); err != nil {
			t.Fatalf("pin bstat-pinned: %v", err)
		}

		// Graph-held blocked-status rows: an open blocks edge, an open
		// conditional-blocks edge, a blocked parent, and a held waits-for
		// gate (its spawner still has an open child) each keep is_blocked
		// set, so the stored flags agree with the statuses.
		deps := []*types.Dependency{
			{IssueID: "bstat-held-blocks", DependsOnID: "bstat-blocker", Type: types.DepBlocks},
			{IssueID: "bstat-held-conditional", DependsOnID: "bstat-blocker", Type: types.DepConditionalBlocks},
			{IssueID: "bstat-parent-blocker", DependsOnID: "bstat-blocker", Type: types.DepBlocks},
			{IssueID: "bstat-child", DependsOnID: "bstat-parent-blocker", Type: types.DepParentChild},
			{IssueID: "bstat-gate-child", DependsOnID: "bstat-gate", Type: types.DepParentChild},
			{IssueID: "bstat-held-waits", DependsOnID: "bstat-gate", Type: types.DepWaitsFor},
		}
		for _, dep := range deps {
			if err := store.AddDependency(ctx, dep, "test"); err != nil {
				t.Fatalf("AddDependency(%s -> %s, %s): %v", dep.IssueID, dep.DependsOnID, dep.Type, err)
			}
		}
		for _, id := range []string{"bstat-held-blocks", "bstat-held-conditional", "bstat-child", "bstat-held-waits"} {
			blockedStatusHold(t, store, id)
			requireStoredIsBlocked(t, store, id, true)
		}

		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusOK {
			t.Fatalf("Status = %q (%s / %s), want %q: graph-held holds are not drift", check.Status, check.Message, check.Detail, StatusOK)
		}
		for _, id := range []string{"bstat-held-blocks", "bstat-held-conditional", "bstat-child", "bstat-held-waits"} {
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				t.Fatalf("GetIssue(%s): %v", id, err)
			}
			if issue.Status != types.StatusBlocked {
				t.Errorf("%s status = %q after the check, want 'blocked' untouched", id, issue.Status)
			}
		}
	})

	t.Run("ephemeral_wisp_hold_is_reported", func(t *testing.T) {
		store := newTestDoltStore(t, "bstat")
		wisp := newTestIssue("bstat-wisp")
		wisp.Ephemeral = true
		if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
			t.Fatalf("CreateIssue(wisp): %v", err)
		}
		if _, err := store.UnderlyingDB().ExecContext(ctx,
			"UPDATE wisps SET status = ? WHERE id = ?", string(types.StatusBlocked), wisp.ID); err != nil {
			t.Fatalf("hold the wisp's status: %v", err)
		}

		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusWarning || !strings.Contains(check.Detail, wisp.ID) {
			t.Fatalf("check = %q (%s / %s), want a warning naming the wisp %s", check.Status, check.Message, check.Detail, wisp.ID)
		}
	})

	t.Run("seven_holds_list_five_examples_and_the_remainder", func(t *testing.T) {
		store := newTestDoltStore(t, "bstat")
		for i := 0; i < 7; i++ {
			id := "bstat-multi-" + string(rune('a'+i))
			if err := store.CreateIssue(ctx, newTestIssue(id), "test"); err != nil {
				t.Fatalf("CreateIssue(%s): %v", id, err)
			}
			blockedStatusHold(t, store, id)
		}

		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusWarning {
			t.Fatalf("Status = %q (%s), want %q", check.Status, check.Message, StatusWarning)
		}
		if want := "7 issue(s) remain in status 'blocked' while their stored dependency-blocked flag is clear"; check.Message != want {
			t.Errorf("Message = %q, want %q", check.Message, want)
		}
		if want := "bstat-multi-a, bstat-multi-b, bstat-multi-c, bstat-multi-d, bstat-multi-e (+2 more)"; check.Detail != want {
			t.Errorf("Detail = %q, want %q", check.Detail, want)
		}
	})

	t.Run("absent_database_is_ok_not_clean", func(t *testing.T) {
		if check := CheckBlockedStatus(ctx, nil); check.Status != StatusOK || check.Message != "No database yet" {
			t.Errorf("nil searcher: %q (%s), want ok \"No database yet\"", check.Status, check.Message)
		}
		if check := CheckBlockedStatusWithStore(nil); check.Status != StatusOK || check.Message != "No database yet" {
			t.Errorf("nil shared store: %q (%s), want ok \"No database yet\"", check.Status, check.Message)
		}
		if check := CheckBlockedStatusWithStore(&SharedStore{}); check.Status != StatusOK || check.Message != "No database yet" {
			t.Errorf("unopened shared store: %q (%s), want ok \"No database yet\"", check.Status, check.Message)
		}
	})

	t.Run("search_failure_is_a_warning_never_clean", func(t *testing.T) {
		boom := errors.New("dial tcp: connection refused")
		check := CheckBlockedStatus(ctx, errorSearcher{err: boom})
		if check.Status != StatusWarning {
			t.Fatalf("Status = %q, want %q", check.Status, StatusWarning)
		}
		if !strings.Contains(check.Detail, boom.Error()) {
			t.Errorf("Detail = %q, want it to name the failure", check.Detail)
		}
	})

	t.Run("stale_flag_stays_unreported_until_recomputed", func(t *testing.T) {
		store := newTestDoltStore(t, "bstat")
		if err := store.CreateIssue(ctx, newTestIssue("bstat-stale"), "test"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		blockedStatusHold(t, store, "bstat-stale")
		// A stale flag held the other way: the graph holds nothing blocked,
		// but is_blocked still says 1. The check reports stored flags, so it
		// reports nothing — accurately, as its ok wording names the stored
		// flag — and mutates nothing. (The raw UPDATE is committed first:
		// RecomputeAllBlocked refuses a dirty working set, and only raw SQL
		// bypasses the store's per-write commits.)
		if _, err := store.UnderlyingDB().ExecContext(ctx,
			"UPDATE issues SET is_blocked = 1 WHERE id = ?", "bstat-stale"); err != nil {
			t.Fatalf("plant the stale flag: %v", err)
		}
		if committed, err := store.CommitAll(ctx, "test: plant a stale is_blocked flag"); err != nil || !committed {
			t.Fatalf("commit the planted flag (committed=%v): %v", committed, err)
		}

		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusOK {
			t.Fatalf("Status = %q (%s / %s), want %q while the stored flag says blocked", check.Status, check.Message, check.Detail, StatusOK)
		}
		if !strings.Contains(check.Message, "stored dependency-blocked flag") {
			t.Errorf("ok wording must name the stored flag it reports, got %q", check.Message)
		}
		requireStoredIsBlocked(t, store, "bstat-stale", true)

		// The existing recompute is what reconciles the flag; afterwards the
		// same check surfaces the hold.
		if _, err := store.RecomputeAllBlocked(ctx); err != nil {
			t.Fatalf("RecomputeAllBlocked: %v", err)
		}
		requireStoredIsBlocked(t, store, "bstat-stale", false)
		check = CheckBlockedStatus(ctx, store)
		if check.Status != StatusWarning || !strings.Contains(check.Detail, "bstat-stale") {
			t.Fatalf("after recompute: %q (%s / %s), want a warning naming bstat-stale", check.Status, check.Message, check.Detail)
		}
	})
}
