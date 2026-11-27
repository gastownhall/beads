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

// TestDanglingWispEdge_MarkOnlyPathKeepsAWaiterOnAnAbsentSpawner is the
// mark-only path (MarkIsBlockedInTx, which every blocks, conditional-blocks and
// waits-for edge add runs over its source and the source's subtree) over the
// waiter of an any-children gate onto a wisp spawner this database does not
// have. On the minting database the spawner's closed wisp child opens the gate,
// so the waiter is clear. Here only the spawner's open durable child is
// visible, and read alone it would close the gate. The waiter is undecided, so
// no recompute would ever take back a mark made on that partial view: the
// write must leave it as found, journal no flip for it, and leave the full
// repair nothing to correct. Each arm reaches the mark-only pass its own way:
// directly; through a waits-for add on the waiter onto a spawner with no
// children (no reason); and through a blocks add on the waiter's parent onto
// a closed issue, which gives the parent no reason, so the pass runs over the
// parent's subtree.
func TestDanglingWispEdge_MarkOnlyPathKeepsAWaiterOnAnAbsentSpawner(t *testing.T) {
	for _, arm := range []struct {
		name, prefix string
		write        func(ctx context.Context, t *testing.T, store *DoltStore, prefix string)
	}{
		{"MarkIsBlockedInTx", "dmm", func(ctx context.Context, t *testing.T, store *DoltStore, prefix string) {
			inJournaledTx(ctx, t, store, "MarkIsBlockedInTx", func(tx *sql.Tx) error {
				return issueops.MarkIsBlockedInTx(ctx, tx, []string{prefix + "-a"}, nil, issueops.NoRequestActor)
			})
		}},
		{"waits-for add on the waiter", "dmw", func(ctx context.Context, t *testing.T, store *DoltStore, prefix string) {
			createPerm(t, ctx, store, prefix+"-z")
			addDependencyWithMeta(t, ctx, store, prefix+"-a", prefix+"-z", types.DepWaitsFor, "")
		}},
		{"blocks add on the waiter's parent", "dmb", func(ctx context.Context, t *testing.T, store *DoltStore, prefix string) {
			createPerm(t, ctx, store, prefix+"-y")
			if err := store.CloseIssue(ctx, prefix+"-y", "done", "tester", ""); err != nil {
				t.Fatalf("close %s-y: %v", prefix, err)
			}
			addDependencyWithMeta(t, ctx, store, prefix+"-e", prefix+"-y", types.DepBlocks, "")
		}},
	} {
		t.Run(arm.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			a, e, s, k, kw := arm.prefix+"-a", arm.prefix+"-e", arm.prefix+"-s", arm.prefix+"-k", arm.prefix+"-kw"

			createPerm(t, ctx, store, e)
			createPerm(t, ctx, store, a)
			createWisp(t, ctx, store, s)
			createPerm(t, ctx, store, k)
			createWisp(t, ctx, store, kw)
			addDependencyWithMeta(t, ctx, store, a, e, types.DepParentChild, "")
			addDependencyWithMeta(t, ctx, store, k, s, types.DepParentChild, "")
			addDependencyWithMeta(t, ctx, store, kw, s, types.DepParentChild, "")
			addDependencyWithMeta(t, ctx, store, a, s, types.DepWaitsFor, `{"gate":"any-children"}`)
			if err := store.CloseIssue(ctx, kw, "done", "tester", ""); err != nil {
				t.Fatalf("close wisp child: %v", err)
			}
			minting := map[string]bool{a: false, e: false, k: false}
			assertBlockedFlags(ctx, t, store, "minting database, one child closed", minting)
			assertConvergedAndStable(ctx, t, store)

			loseWisp(ctx, t, store, kw)
			loseWisp(ctx, t, store, s)
			assertLeftAsFound(ctx, t, store, "spawner absent", minting, a)

			enableJournalForTest(t, store)
			clearJournal(t, store)
			arm.write(ctx, t, store, arm.prefix)
			assertBlockedFlags(ctx, t, store, "after the write", minting)
			for _, r := range readJournalRows(t, store) {
				if r.id == a && r.op == string(issueops.EventUpdate) {
					t.Errorf("the write journaled an is_blocked flip for held %s", a)
				}
			}
			assertLeftAsFound(ctx, t, store, "after the write", minting, a)
		})
	}
}
