//go:build cgo

package doctor

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCheckBlockedConsistencyDB_ReportsRowsLeftAsFound runs the Blocked State
// check over a database holding an issue blocked on a wisp it has no row for,
// the state of a clone: the row is not stale, so the check stays OK and adds
// the count of rows it leaves as found. A stale flag elsewhere turns the check
// into a warning, and the count rides in its Detail.
func TestCheckBlockedConsistencyDB_ReportsRowsLeftAsFound(t *testing.T) {
	store := newTestDoltStore(t, "test")
	db := store.UnderlyingDB()
	ctx := context.Background()

	createCycleIssues(t, store, "test-a", "test-s")
	createCycleWisps(t, store, "test-w")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "test-a", DependsOnID: "test-w", Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	// The clone's state: the edge replicated, the wisp did not.
	execCycleFixture(t, db, "DELETE FROM wisps WHERE id = 'test-w'")

	check := checkBlockedConsistencyDB(ctx, db)
	wantMessage := "is_blocked flags consistent with dependency graph (" + undecidedBlockedNote(1) + ")"
	if check.Status != StatusOK || check.Message != wantMessage || check.Detail != undecidedBlockedDetail {
		t.Errorf("no stale flag: got (%q, %q, %q)\nwant (%q, %q, %q)",
			check.Status, check.Message, check.Detail, StatusOK, wantMessage, undecidedBlockedDetail)
	}

	execCycleFixture(t, db, "UPDATE issues SET is_blocked = 1 WHERE id = 'test-s'")
	check = checkBlockedConsistencyDB(ctx, db)
	wantMessage = "1 issue/wisp row(s) have a stale is_blocked flag — 'bd ready' may hide ready work or show blocked work"
	wantDetail := "is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale; " +
		undecidedBlockedNote(1) + ". " + undecidedBlockedDetail
	if check.Status != StatusWarning || check.Message != wantMessage || check.Detail != wantDetail {
		t.Errorf("one stale flag: got (%q, %q, %q)\nwant (%q, %q, %q)",
			check.Status, check.Message, check.Detail, StatusWarning, wantMessage, wantDetail)
	}
}
