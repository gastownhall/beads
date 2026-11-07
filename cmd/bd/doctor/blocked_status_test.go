//go:build cgo

package doctor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// failingSearcher is an IssueSearcher whose every search fails.
type failingSearcher struct{ err error }

func (f failingSearcher) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, f.err
}

// TestCheckBlockedStatus pins the Blocked Status advisory (bd-e5d8fd): it
// names the issues whose explicit status is 'blocked' while their stored
// is_blocked flag is clear, bounded to five IDs, and changes no status.
func TestCheckBlockedStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("no_database", func(t *testing.T) {
		check := CheckBlockedStatus(ctx, nil)
		if check.Name != BlockedStatusCheckName || check.Status != StatusOK || check.Message != "No database yet" {
			t.Errorf("CheckBlockedStatus(nil) = %s %s %q, want %s ok \"No database yet\"", check.Name, check.Status, check.Message, BlockedStatusCheckName)
		}
		if check := CheckBlockedStatusWithStore(nil); check.Status != StatusOK || check.Message != "No database yet" {
			t.Errorf("nil shared store: %s %q, want ok \"No database yet\"", check.Status, check.Message)
		}
		if check := CheckBlockedStatusWithStore(&SharedStore{}); check.Status != StatusOK || check.Message != "No database yet" {
			t.Errorf("unopened shared store: %s %q, want ok \"No database yet\"", check.Status, check.Message)
		}
	})

	t.Run("search_error_is_a_warning", func(t *testing.T) {
		check := CheckBlockedStatus(ctx, failingSearcher{err: errors.New("server gone")})
		if check.Status != StatusWarning || check.Message != "Unable to check blocked statuses" || check.Detail != "server gone" {
			t.Errorf("a failed search reports %s %q (%s), want a warning naming the error, never a clean result", check.Status, check.Message, check.Detail)
		}
	})

	// no_dependency_recorded and last_blocker_closed are the two stranded
	// shapes PR #6565 (@quad341) reported; this check names them for review
	// instead of repairing them.
	t.Run("no_dependency_recorded", func(t *testing.T) {
		store := newTestDoltStore(t, "bsn")
		held := createBlockedStatusIssue(t, store, &types.Issue{Title: "held by hand", Status: types.StatusBlocked})
		check := CheckBlockedStatus(ctx, store)
		requireBlockedStatusWarning(t, check, 1, []string{held.ID}, 0)
		// The adapter the full doctor uses reports the same thing.
		if adapted := CheckBlockedStatusWithStore(&SharedStore{store: store}); adapted.Status != check.Status || adapted.Message != check.Message || adapted.Detail != check.Detail {
			t.Errorf("CheckBlockedStatusWithStore = %s %q (%s), want the same warning", adapted.Status, adapted.Message, adapted.Detail)
		}
		requireStatus(t, store, held.ID, types.StatusBlocked)
	})

	t.Run("last_blocker_closed", func(t *testing.T) {
		store := newTestDoltStore(t, "bsc")
		blocker := createBlockedStatusIssue(t, store, &types.Issue{Title: "blocker", Status: types.StatusOpen})
		held := createBlockedStatusIssue(t, store, &types.Issue{Title: "held", Status: types.StatusBlocked})
		addBlockedStatusDep(t, store, held.ID, blocker.ID, types.DepBlocks)
		if err := store.CloseIssue(ctx, blocker.ID, "done", "test", ""); err != nil {
			t.Fatal(err)
		}
		check := CheckBlockedStatus(ctx, store)
		requireBlockedStatusWarning(t, check, 1, []string{held.ID}, 0)
		requireStatus(t, store, held.ID, types.StatusBlocked)
	})

	t.Run("wisps_and_no_history_beads", func(t *testing.T) {
		store := newTestDoltStore(t, "bsw")
		wisp := createBlockedStatusIssue(t, store, &types.Issue{Title: "blocked wisp", Status: types.StatusBlocked, Ephemeral: true})
		noHistory := createBlockedStatusIssue(t, store, &types.Issue{Title: "blocked no-history bead", Status: types.StatusBlocked, NoHistory: true})
		check := CheckBlockedStatus(ctx, store)
		requireBlockedStatusWarning(t, check, 2, sortedIDs(wisp.ID, noHistory.ID), 0)
	})

	t.Run("seven_matches_name_five", func(t *testing.T) {
		store := newTestDoltStore(t, "bss")
		var ids []string
		for i := 0; i < 7; i++ {
			held := createBlockedStatusIssue(t, store, &types.Issue{Title: fmt.Sprintf("held %d", i), Status: types.StatusBlocked})
			ids = append(ids, held.ID)
		}
		check := CheckBlockedStatus(ctx, store)
		requireBlockedStatusWarning(t, check, 7, sortedIDs(ids...)[:5], 2)
		for _, id := range ids {
			requireStatus(t, store, id, types.StatusBlocked)
		}
	})

	// stale_true_flag_stays_unreported_until_recomputed: nothing in the graph
	// holds the row, but its stored is_blocked still says 1. The check reports
	// stored flags, so it reports nothing (its ok wording names the stored
	// flag) and mutates nothing; after the existing recompute it names the row.
	t.Run("stale_true_flag_stays_unreported_until_recomputed", func(t *testing.T) {
		store := newTestDoltStore(t, "bst")
		held := createBlockedStatusIssue(t, store, &types.Issue{Title: "held", Status: types.StatusBlocked})
		// RecomputeAllBlocked refuses a dirty working set, so the raw write
		// is committed before the recompute below.
		if _, err := store.DB().ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", held.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.Commit(ctx, "test: leave a stale is_blocked flag"); err != nil {
			t.Fatal(err)
		}
		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusOK || !strings.Contains(check.Message, "stored dependency-blocked flag") {
			t.Errorf("check = %s %q (%s), want ok naming the stored flag it reads", check.Status, check.Message, check.Detail)
		}
		requireStatus(t, store, held.ID, types.StatusBlocked)

		if _, err := store.RecomputeAllBlocked(ctx); err != nil {
			t.Fatalf("RecomputeAllBlocked: %v", err)
		}
		requireBlockedStatusWarning(t, CheckBlockedStatus(ctx, store), 1, []string{held.ID}, 0)
		requireStatus(t, store, held.ID, types.StatusBlocked)
	})

	// graph_held_and_ordinary_rows_are_ok: with consistent flags, every
	// status-blocked row the graph holds (an open blocks or conditional-blocks
	// target, a blocked parent, a held waits-for gate) and the ordinary open,
	// closed and pinned rows beside them report nothing, and keep their
	// statuses.
	t.Run("graph_held_and_ordinary_rows_are_ok", func(t *testing.T) {
		store := newTestDoltStore(t, "bso")
		blocker := createBlockedStatusIssue(t, store, &types.Issue{Title: "open blocker", Status: types.StatusOpen})
		heldByBlocks := createBlockedStatusIssue(t, store, &types.Issue{Title: "held by blocks", Status: types.StatusBlocked})
		addBlockedStatusDep(t, store, heldByBlocks.ID, blocker.ID, types.DepBlocks)
		heldByConditional := createBlockedStatusIssue(t, store, &types.Issue{Title: "held by conditional-blocks", Status: types.StatusBlocked})
		addBlockedStatusDep(t, store, heldByConditional.ID, blocker.ID, types.DepConditionalBlocks)
		parent := createBlockedStatusIssue(t, store, &types.Issue{Title: "blocked parent", Status: types.StatusOpen, IssueType: types.TypeEpic})
		addBlockedStatusDep(t, store, parent.ID, blocker.ID, types.DepBlocks)
		heldByParent := createBlockedStatusIssue(t, store, &types.Issue{Title: "held by its parent", Status: types.StatusBlocked})
		addBlockedStatusDep(t, store, heldByParent.ID, parent.ID, types.DepParentChild)
		// A waits-for gate holds while its spawner still has an open child.
		spawner := createBlockedStatusIssue(t, store, &types.Issue{Title: "spawner", Status: types.StatusOpen})
		spawned := createBlockedStatusIssue(t, store, &types.Issue{Title: "active child of the spawner", Status: types.StatusOpen})
		addBlockedStatusDep(t, store, spawned.ID, spawner.ID, types.DepParentChild)
		heldByGate := createBlockedStatusIssue(t, store, &types.Issue{Title: "held by a waits-for gate", Status: types.StatusBlocked})
		addBlockedStatusDep(t, store, heldByGate.ID, spawner.ID, types.DepWaitsFor)
		open := createBlockedStatusIssue(t, store, &types.Issue{Title: "ordinary open", Status: types.StatusOpen})
		closed := createBlockedStatusIssue(t, store, &types.Issue{Title: "ordinary closed", Status: types.StatusOpen})
		if err := store.CloseIssue(ctx, closed.ID, "done", "test", ""); err != nil {
			t.Fatal(err)
		}
		pinned := createBlockedStatusIssue(t, store, &types.Issue{Title: "ordinary pinned", Status: types.StatusPinned})
		for _, id := range []string{heldByBlocks.ID, heldByConditional.ID, heldByParent.ID, heldByGate.ID} {
			requireStoredIsBlocked(t, store, id, true)
		}

		check := CheckBlockedStatus(ctx, store)
		if check.Status != StatusOK {
			t.Errorf("check = %s %q (%s), want ok: every status-blocked row here is held by the graph", check.Status, check.Message, check.Detail)
		}
		for id, want := range map[string]types.Status{
			heldByBlocks.ID: types.StatusBlocked, heldByConditional.ID: types.StatusBlocked, heldByParent.ID: types.StatusBlocked,
			heldByGate.ID: types.StatusBlocked, spawner.ID: types.StatusOpen, spawned.ID: types.StatusOpen,
			blocker.ID: types.StatusOpen, parent.ID: types.StatusOpen, open.ID: types.StatusOpen,
			closed.ID: types.StatusClosed, pinned.ID: types.StatusPinned,
		} {
			requireStatus(t, store, id, want)
		}
	})

	// stale_flag_reports_the_stored_state: the check reads the stored flag,
	// so a held row whose is_blocked was left clear is named (and kept as it
	// is) until the existing recompute repairs the flag; rerun, it is ok.
	t.Run("stale_flag_reports_the_stored_state", func(t *testing.T) {
		store := newTestDoltStore(t, "bsf")
		blocker := createBlockedStatusIssue(t, store, &types.Issue{Title: "open blocker", Status: types.StatusOpen})
		held := createBlockedStatusIssue(t, store, &types.Issue{Title: "held", Status: types.StatusBlocked})
		addBlockedStatusDep(t, store, held.ID, blocker.ID, types.DepBlocks)
		if _, err := store.DB().ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = ?", held.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.Commit(ctx, "test: leave a stale is_blocked flag"); err != nil {
			t.Fatal(err)
		}
		requireBlockedStatusWarning(t, CheckBlockedStatus(ctx, store), 1, []string{held.ID}, 0)
		requireStatus(t, store, held.ID, types.StatusBlocked)

		if _, err := store.RecomputeAllBlocked(ctx); err != nil {
			t.Fatalf("RecomputeAllBlocked: %v", err)
		}
		if check := CheckBlockedStatus(ctx, store); check.Status != StatusOK {
			t.Errorf("after the recompute the check = %s %q (%s), want ok", check.Status, check.Message, check.Detail)
		}
		requireStatus(t, store, held.ID, types.StatusBlocked)
	})

	// wisp_blocker_is_honoured_until_it_closes: a durable status-blocked
	// issue held by an open wisp is not reported; once the wisp closes the
	// same issue is, and its status stays as it is throughout. Adapted from
	// PR #6565's TestStatusBlockedDrift_WispBlockerIsHonouredUntilItCloses
	// (@quad341), without that PR's repair.
	t.Run("wisp_blocker_is_honoured_until_it_closes", func(t *testing.T) {
		store := newTestDoltStore(t, "bsb")
		wisp := createBlockedStatusIssue(t, store, &types.Issue{Title: "wisp blocker", Status: types.StatusOpen, Ephemeral: true})
		held := createBlockedStatusIssue(t, store, &types.Issue{Title: "held by a wisp", Status: types.StatusBlocked})
		addBlockedStatusDep(t, store, held.ID, wisp.ID, types.DepBlocks)
		requireStoredIsBlocked(t, store, held.ID, true)

		if check := CheckBlockedStatus(ctx, store); check.Status != StatusOK {
			t.Errorf("wisp blocker open: check = %s %q (%s), want ok", check.Status, check.Message, check.Detail)
		}
		requireStatus(t, store, held.ID, types.StatusBlocked)

		if err := store.CloseIssue(ctx, wisp.ID, "done", "test", ""); err != nil {
			t.Fatalf("close the wisp blocker: %v", err)
		}
		requireBlockedStatusWarning(t, CheckBlockedStatus(ctx, store), 1, []string{held.ID}, 0)
		requireStatus(t, store, held.ID, types.StatusBlocked)
	})
}

// requireBlockedStatusWarning asserts check warns of count issues, naming
// examples in order and then "(+more more)" when more is not zero, with the
// manual guidance and no repair of its own.
func requireBlockedStatusWarning(t *testing.T, check DoctorCheck, count int, examples []string, more int) {
	t.Helper()
	if check.Name != BlockedStatusCheckName || check.Status != StatusWarning {
		t.Fatalf("check = %s %s %q (%s), want a %s warning", check.Name, check.Status, check.Message, check.Detail, BlockedStatusCheckName)
	}
	wantMessage := fmt.Sprintf("%d issue(s) remain in status 'blocked' while their stored dependency-blocked flag is clear", count)
	if check.Message != wantMessage {
		t.Errorf("message = %q, want %q", check.Message, wantMessage)
	}
	wantIDs := strings.Join(examples, ", ")
	if more > 0 {
		wantIDs += fmt.Sprintf(" (+%d more)", more)
	}
	if !strings.HasPrefix(check.Detail, wantIDs+". ") {
		t.Errorf("detail = %q, want it to start with %q", check.Detail, wantIDs+". ")
	}
	for _, want := range []string{"bd update <id>... --status open", "bd dep add <id> <blocker-id>", "tracker"} {
		if !strings.Contains(check.Fix, want) {
			t.Errorf("fix = %q, want it to name %q", check.Fix, want)
		}
	}
}

func createBlockedStatusIssue(t *testing.T, store *dolt.DoltStore, issue *types.Issue) *types.Issue {
	t.Helper()
	issue.Priority = 2
	if issue.IssueType == "" {
		issue.IssueType = types.TypeTask
	}
	if err := store.CreateIssue(context.Background(), issue, "test"); err != nil {
		t.Fatalf("create %q: %v", issue.Title, err)
	}
	return issue
}

func addBlockedStatusDep(t *testing.T, store *dolt.DoltStore, issueID, dependsOnID string, depType types.DependencyType) {
	t.Helper()
	dep := &types.Dependency{IssueID: issueID, DependsOnID: dependsOnID, Type: depType}
	if err := store.AddDependency(context.Background(), dep, "test"); err != nil {
		t.Fatalf("add %s %s→%s: %v", depType, issueID, dependsOnID, err)
	}
}

func requireStatus(t *testing.T, store *dolt.DoltStore, id string, want types.Status) {
	t.Helper()
	got, err := store.GetIssue(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if got.Status != want {
		t.Errorf("%s status = %q, want %q: the advisory changes no status", id, got.Status, want)
	}
}

func sortedIDs(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// requireStoredIsBlocked pins a fixture's stored is_blocked flag before the
// check runs, so a case meant to be consistent (or drifted) fails on its
// setup, not on the check under test.
func requireStoredIsBlocked(t *testing.T, store *dolt.DoltStore, id string, want bool) {
	t.Helper()
	var got bool
	if err := store.DB().QueryRowContext(context.Background(),
		"SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("read is_blocked(%s): %v", id, err)
	}
	if got != want {
		t.Fatalf("precondition: %s stored is_blocked = %v, want %v", id, got, want)
	}
}
