package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// inJournaledTx runs fn in one transaction with the events journal on, the way
// a store scopes it to the transaction it begins.
func inJournaledTx(ctx context.Context, t *testing.T, store *DoltStore, what string, fn func(tx *sql.Tx) error) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("%s: begin: %v", what, err)
	}
	done := issueops.ScopeEventsJournalTransaction(tx, true)
	defer done()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("%s: %v", what, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("%s: commit: %v", what, err)
	}
}

// issuesLeftToCommit reports whether the working set holds an issues change.
func issuesLeftToCommit(ctx context.Context, t *testing.T, store *DoltStore) bool {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'").Scan(&n); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	return n > 0
}

// journaledIDs returns the ids the events journal holds a row for.
func journaledIDs(t *testing.T, store *DoltStore) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, r := range readJournalRows(t, store) {
		out[r.id] = true
	}
	return out
}

// TestDanglingWispEdge_MergedValueKeptByTheScopedPaths is the pull: a merge
// brings the minting database's flag for a held row A and for A's child C, and
// the post-merge recompute, the scoped recompute and the mark-only path over A
// and its subtree all keep it. Nothing is left in the working set to commit
// back, and the journal records no flip for them. A decided row D, made stale
// by the same kind of merge, is corrected and journaled by the same call, so
// the journal check is not vacuous. Before the fix the post-merge recompute
// cleared a merged 1, and the clone's next commit sent the 0 back.
func TestDanglingWispEdge_MergedValueKeptByTheScopedPaths(t *testing.T) {
	for _, merged := range []bool{true, false} {
		t.Run(fmt.Sprintf("merged %v", merged), func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			createPerm(t, ctx, store, "dmg-a")
			createPerm(t, ctx, store, "dmg-c")
			createPerm(t, ctx, store, "dmg-d")
			createPerm(t, ctx, store, "dmg-x")
			createWisp(t, ctx, store, "dmg-w")
			addDependencyWithMeta(t, ctx, store, "dmg-a", "dmg-w", types.DepBlocks, "")
			addDependencyWithMeta(t, ctx, store, "dmg-c", "dmg-a", types.DepParentChild, "")
			addDependencyWithMeta(t, ctx, store, "dmg-d", "dmg-x", types.DepBlocks, "")
			loseWisp(ctx, t, store, "dmg-w")

			// Before the pull the clone holds the other value for A and C.
			plantIsBlocked(ctx, t, store, "dmg-a", !merged)
			plantIsBlocked(ctx, t, store, "dmg-c", !merged)
			commitAll(ctx, t, store, "clone before the pull")
			preHead, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("read pre-merge HEAD: %v", err)
			}

			// The merge brings the minting database's flags, and a stale 0 on D.
			plantIsBlocked(ctx, t, store, "dmg-a", merged)
			plantIsBlocked(ctx, t, store, "dmg-c", merged)
			plantIsBlocked(ctx, t, store, "dmg-d", false)
			commitAll(ctx, t, store, "merge")
			clearJournal(t, store)

			want := map[string]bool{"dmg-a": merged, "dmg-c": merged, "dmg-d": true}
			inJournaledTx(ctx, t, store, "RecomputeIsBlockedAfterMergeInTx", func(tx *sql.Tx) error {
				return issueops.RecomputeIsBlockedAfterMergeInTx(ctx, tx, preHead)
			})
			assertBlockedFlags(ctx, t, store, "after the post-merge recompute", want)
			got := journaledIDs(t, store)
			if !got["dmg-d"] {
				t.Errorf("the post-merge recompute corrected dmg-d but journaled %v; the journal check below would prove nothing", got)
			}
			for _, id := range []string{"dmg-a", "dmg-c"} {
				if got[id] {
					t.Errorf("the post-merge recompute journaled a flip for held %s", id)
				}
			}
			commitAll(ctx, t, store, "settle the decided row")

			for _, step := range []struct {
				name string
				run  func(tx *sql.Tx) error
			}{
				{"RecomputeIsBlockedInTx", func(tx *sql.Tx) error {
					return issueops.RecomputeIsBlockedInTx(ctx, tx, []string{"dmg-a", "dmg-c"}, nil, issueops.NoRequestActor)
				}},
				{"MarkIsBlockedInTx", func(tx *sql.Tx) error {
					return issueops.MarkIsBlockedInTx(ctx, tx, []string{"dmg-a", "dmg-c"}, nil, issueops.NoRequestActor)
				}},
			} {
				clearJournal(t, store)
				inJournaledTx(ctx, t, store, step.name, step.run)
				assertBlockedFlags(ctx, t, store, "after "+step.name, want)
				if issuesLeftToCommit(ctx, t, store) {
					t.Errorf("%s left an issues change in the working set", step.name)
				}
				if got := journaledIDs(t, store); got["dmg-a"] || got["dmg-c"] {
					t.Errorf("%s journaled a flip for a held row: %v", step.name, got)
				}
			}
		})
	}
}
