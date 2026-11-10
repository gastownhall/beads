package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCommitToleratesDirtyIgnoredWisp locks in that a dirty dolt_ignore'd table
// (a wisp) is invisible to the commit path: HasCommittablePending reports
// nothing to commit and Commit succeeds without staging or destroying it.
//
// A dirty wisp is the normal steady state — creating an ephemeral issue writes
// the wisps table and deliberately does not DOLT_COMMIT it (issues.go). Both
// commitWorkingSet and HasCommittablePending exclude such tables with a
// dolt_ignore anti-join; before that filter, commitWorkingSet fed every
// dolt_status row into a fail-hard DOLT_ADD loop, so an ordinary Commit could
// fail (or its behavior silently depended on Dolt's version-specific handling
// of DOLT_ADD on an ignored table — a no-op on 2.2.0). This test dirties a wisp
// and asserts Commit() succeeds, so a future Dolt that changes ignored-table
// staging cannot regress that silently.
//
// The assertion bites on this harness: setupTestStore's shared branch-per-test
// database materializes ignored tables at HEAD, so a dirty wisp really does
// surface in dolt_status (the same reason TestCreateIssueCommitsInitialRelational
// Data must exclude the dolt_ignore'd events table from its clean check). Without
// the anti-join, HasCommittablePending would therefore see the wisp and wrongly
// report committable work.
func TestCommitToleratesDirtyIgnoredWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// A durable create auto-commits, leaving a clean committed baseline and
	// proving the store commits real work — so a later "nothing to commit" is
	// the anti-join at work, not a dead store.
	durable := &types.Issue{
		ID:        "dirty-wisp-durable",
		Title:     "Durable baseline",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, durable, "tester"); err != nil {
		t.Fatalf("CreateIssue durable: %v", err)
	}
	requireCleanTables(ctx, t, store, "issues")

	// An ephemeral create writes the wisps table without committing it.
	wisp := &types.Issue{
		ID:        "dirty-wisp-ephemeral",
		Title:     "Ephemeral wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}

	// Precondition: the ignored wisps table is genuinely dirty, so the
	// anti-join is actually exercised rather than passing vacuously. If a future
	// Dolt stops surfacing ignored tables here, this fails loudly — the premise
	// changed and the anti-join's coverage must be re-examined.
	if !tableDirty(ctx, t, store, "wisps") {
		t.Fatal("wisps not dirty after ephemeral create; anti-join would pass vacuously")
	}

	// The wisp is the only dirty table, and it is ignored, so the commit path
	// must see nothing committable.
	pending, err := store.HasCommittablePending(ctx)
	if err != nil {
		t.Fatalf("HasCommittablePending: %v", err)
	}
	if pending {
		t.Fatal("HasCommittablePending = true with only a dirty ignored wisp; anti-join not applied")
	}

	// The reviewer's core ask: Commit tolerates the dirty ignored table.
	if err := store.Commit(ctx, "commit over a dirty wisp"); err != nil {
		t.Fatalf("Commit over a dirty ignored wisp: %v", err)
	}

	// Commit left the ignored table exactly as it was — neither staged into a
	// commit nor discarded. The wisp is still dirty and still readable.
	if !tableDirty(ctx, t, store, "wisps") {
		t.Fatal("wisps no longer dirty after Commit; the ignored table was touched")
	}
	if _, err := store.GetIssue(ctx, wisp.ID); err != nil {
		t.Fatalf("GetIssue wisp after Commit: %v", err)
	}
}

// tableDirty reports whether a table shows uncommitted changes in dolt_status.
func tableDirty(ctx context.Context, t *testing.T, store *DoltStore, table string) bool {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = ?", table).Scan(&n); err != nil {
		t.Fatalf("query dolt_status for %s: %v", table, err)
	}
	return n > 0
}
