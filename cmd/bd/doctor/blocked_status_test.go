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
	issue.IssueType = types.TypeTask
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
