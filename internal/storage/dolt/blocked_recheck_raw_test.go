package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil/sqlgate"
	"github.com/steveyegge/beads/internal/types"
)

// seedBlockedDependent creates blockers a and b (wisps when wisp is set) and
// the permanent dependent c that both block.
func seedBlockedDependent(t *testing.T, ctx context.Context, store *DoltStore, a, b, c string, wisp bool) {
	t.Helper()
	for _, id := range []string{a, b} {
		if wisp {
			createWisp(t, ctx, store, id)
		} else {
			createPerm(t, ctx, store, id)
		}
	}
	createPerm(t, ctx, store, c)
	addDependency(t, ctx, store, c, a, types.DepBlocks)
	addDependency(t, ctx, store, c, b, types.DepBlocks)
	assertIsBlocked(t, ctx, store, "issues", c, true)
}

// closeOnSeparatePool closes id and commits it on a pool of its own, with the
// ordinary in-transaction recompute. The store's own transaction may hold
// every connection its pool has, so the sibling writer cannot borrow one.
func closeOnSeparatePool(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	db, err := sql.Open("mysql", store.connStr)
	if err != nil {
		t.Fatalf("open sibling pool: %v", err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin sibling tx: %v", err)
	}
	if _, err := issueops.CloseIssueInTx(ctx, tx, id, "done", "sibling", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("sibling close of %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit sibling close of %s: %v", id, err)
	}
}

func assertReady(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	assertIsBlocked(t, ctx, store, "issues", id, false)
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if !slices.ContainsFunc(ready, func(i *types.Issue) bool { return i.ID == id }) {
		t.Fatalf("%s has no open blockers but is missing from ready work", id)
	}
}

// TestBlockedRecheck_RunInTransactionCommittingLast is gastownhall/beads#6716
// on DoltStore.RunInTransaction, the storage.Transaction runner `bd batch`,
// `bd cook`, `bd mol squash`/`burn` and SDK callers (Gas City's native store
// closes and status updates) write through. The transaction closes one
// blocker while both are open, a sibling close of the other commits, and the
// transaction commits last: without a post-commit recheck the dependent stays
// blocked.
func TestBlockedRecheck_RunInTransactionCommittingLast(t *testing.T) {
	cases := []struct {
		name  string
		wisp  bool
		write func(ctx context.Context, tx storage.Transaction, id string) error
	}{
		{"close", false, func(ctx context.Context, tx storage.Transaction, id string) error {
			return tx.CloseIssue(ctx, id, "done", "tester", "")
		}},
		{"update to closed", false, func(ctx context.Context, tx storage.Transaction, id string) error {
			return tx.UpdateIssue(ctx, id, map[string]interface{}{"status": types.StatusClosed}, "tester")
		}},
		{"wisp close", true, func(ctx context.Context, tx storage.Transaction, id string) error {
			return tx.CloseIssue(ctx, id, "done", "tester", "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			seedBlockedDependent(t, ctx, store, "rt-a", "rt-b", "rt-c", tc.wisp)

			if err := store.RunInTransaction(ctx, "close rt-b", func(tx storage.Transaction) error {
				if err := tc.write(ctx, tx, "rt-b"); err != nil {
					return err
				}
				closeOnSeparatePool(t, ctx, store, "rt-a")
				return nil
			}); err != nil {
				t.Fatalf("RunInTransaction: %v", err)
			}

			assertReady(t, ctx, store, "rt-c")
		})
	}
}

// TestBlockedRecheck_ConcurrentWispCloses races the raw-transaction wisp
// close path (closeWisp) against itself. It cannot pin the interleaving the
// way the tests above do, so it only proves the settled outcome: whichever
// close commits last, the dependent ends up ready.
func TestBlockedRecheck_ConcurrentWispCloses(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	for round := 0; round < 5; round++ {
		a, b, c := fmt.Sprintf("rw-%da", round), fmt.Sprintf("rw-%db", round), fmt.Sprintf("rw-%dc", round)
		seedBlockedDependent(t, ctx, store, a, b, c, true)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, id := range []string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = store.CloseIssue(ctx, id, "done", "tester", "")
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d close: %v", round, err)
			}
		}
		assertReady(t, ctx, store, c)
	}
}

// TestBlockedRecheck_RawWritersSettleACommittedWrite pins the recheck wiring
// of the writers that own a raw transaction (gastownhall/beads#7035, finding
// 2): the wisp update, checked update, delete and batch delete, and both arms
// of RemoveDependency. Each scopes its transaction for the recheck and
// settles what the write recorded after its commit, and until now deleting
// that from any one of them turned no test red.
//
// These writers take no body callback, so the runner tests' way of landing a
// sibling write inside the window does not reach them. The store runs on a
// sqlgate pool instead, which holds the labeled write at its commit point.
//
// Every arm is the #6716 interleaving with the store's public method as the
// writer that commits last. The fixture is two blockers a and b of one
// dependent c. The store removes b (closes it, deletes it, or removes the
// c -> b edge) and is held at its SQL commit; a sibling closes a and commits;
// the store's write is released. The store's snapshot predates the sibling,
// so its own recompute leaves c blocked, and the sibling saw b still in place
// and left c alone as well. Only the writer's recheck on a fresh snapshot can
// clear c from there.
//
// The gate's probe runs on the held session, and each arm reads two facts
// through it: that this transaction has applied the write, which no other
// session can see before the commit, so the hold is on the writer's own
// transaction; and that c is still blocked there, so the recompute inside
// the transaction did not settle it.
func TestBlockedRecheck_RawWritersSettleACommittedWrite(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	gate := raceGatedStore(t, store)
	ctx, cancel := testContext(t)
	defer cancel()

	open := string(types.StatusOpen)
	closed := map[string]interface{}{"status": types.StatusClosed}
	for _, arm := range []struct {
		name          string
		prefix        string
		wispBlockers  bool
		wispDependent bool
		// write removes blocker b, or the edge c -> b, through the store.
		write func(ctx context.Context, b, c string) error
		// applied is a query that reads 1 on a transaction that has applied
		// the write and 0 on every other session.
		applied func(b, c string) string
		// source names the write in the Dolt commit its recheck mints. A wisp
		// dependent lives in a dolt-ignored table, so its arm has none.
		source func(b, c string) string
	}{
		{
			name: "updateWisp", prefix: "uw", wispBlockers: true,
			write: func(ctx context.Context, b, _ string) error {
				return store.UpdateIssue(ctx, b, closed, "tester")
			},
			applied: func(b, _ string) string {
				return "SELECT COUNT(*) FROM wisps WHERE id = '" + b + "' AND status = 'closed'"
			},
			source: func(b, _ string) string { return "close of " + b },
		},
		{
			name: "updateWispChecked", prefix: "uc", wispBlockers: true,
			write: func(ctx context.Context, b, _ string) error {
				return store.UpdateIssueChecked(ctx, b, closed, "tester", storage.UpdateIssueOptions{ExpectedStatus: &open})
			},
			applied: func(b, _ string) string {
				return "SELECT COUNT(*) FROM wisps WHERE id = '" + b + "' AND status = 'closed'"
			},
			source: func(b, _ string) string { return "close of " + b },
		},
		{
			name: "deleteWisp", prefix: "dw", wispBlockers: true,
			write: func(ctx context.Context, b, _ string) error { return store.DeleteIssue(ctx, b) },
			applied: func(b, _ string) string {
				return "SELECT 1 - COUNT(*) FROM wisps WHERE id = '" + b + "'"
			},
			source: func(b, _ string) string { return "delete of " + b },
		},
		{
			name: "deleteWispBatchTx", prefix: "db", wispBlockers: true,
			write: func(ctx context.Context, b, _ string) error {
				_, err := store.DeleteIssues(ctx, []string{b}, false, true, false)
				return err
			},
			applied: func(b, _ string) string {
				return "SELECT 1 - COUNT(*) FROM wisps WHERE id = '" + b + "'"
			},
			source: func(b, _ string) string { return "delete of " + b },
		},
		{
			name: "RemoveDependency wisp arm", prefix: "rw", wispDependent: true,
			write: func(ctx context.Context, b, c string) error {
				return store.RemoveDependency(ctx, c, b, "tester")
			},
			// One of c's two edges is left.
			applied: func(_, c string) string {
				return "SELECT 2 - COUNT(*) FROM wisp_dependencies WHERE issue_id = '" + c + "'"
			},
		},
		{
			name: "RemoveDependency durable arm", prefix: "rd",
			write: func(ctx context.Context, b, c string) error {
				return store.RemoveDependency(ctx, c, b, "tester")
			},
			applied: func(_, c string) string {
				return "SELECT 2 - COUNT(*) FROM dependencies WHERE issue_id = '" + c + "'"
			},
			source: func(b, c string) string { return "dependency removal " + c + " -> " + b },
		},
	} {
		t.Run(arm.name, func(t *testing.T) {
			a, b, c := arm.prefix+"-a", arm.prefix+"-b", arm.prefix+"-c"
			for _, id := range []string{a, b} {
				if arm.wispBlockers {
					createWisp(t, ctx, store, id)
				} else {
					createPerm(t, ctx, store, id)
				}
			}
			table := "issues"
			if arm.wispDependent {
				table = "wisps"
				createWisp(t, ctx, store, c)
			} else {
				createPerm(t, ctx, store, c)
			}
			addDependency(t, ctx, store, c, a, types.DepBlocks)
			addDependency(t, ctx, store, c, b, types.DepBlocks)
			assertIsBlocked(t, ctx, store, table, c, true)

			gate.ProbeQuery = "SELECT (" + arm.applied(b, c) + ") AS write_applied, " +
				"(SELECT is_blocked FROM " + table + " WHERE id = '" + c + "') AS dependent_blocked"
			hold := gate.Arm(arm.name)
			defer hold.Release()
			done := make(chan error, 1)
			go func() { done <- arm.write(sqlgate.WithLabel(ctx, arm.name), b, c) }()
			select {
			case <-hold.Reached():
			case err := <-done:
				t.Fatalf("%s returned %v without reaching a commit point", arm.name, err)
			}
			closeOnSeparatePool(t, ctx, store, a)
			hold.Release()
			if err := <-done; err != nil {
				t.Fatalf("%s: a committed write returned %v, want nil", arm.name, err)
			}

			inTx, err := gate.Settings(arm.name)
			if err != nil {
				t.Fatalf("%s: probe the held transaction: %v", arm.name, err)
			}
			if inTx["write_applied"] != "1" {
				t.Fatalf("%s: the held transaction had not applied the write (write_applied = %q), so the sibling close of %s did not land inside the writer's window", arm.name, inTx["write_applied"], a)
			}
			if inTx["dependent_blocked"] != "1" {
				t.Fatalf("%s: %s was already unblocked inside the write's own transaction (is_blocked = %q), so this arm measures nothing about the recheck", arm.name, c, inTx["dependent_blocked"])
			}

			// Nothing here called recheckBlockedAfterCommit or touched the
			// pending set: the writer's own wiring is the only thing that can
			// have settled the dependent.
			assertIsBlocked(t, ctx, store, table, c, false)
			if arm.source == nil {
				return
			}
			assertReady(t, ctx, store, c)
			requireCleanTables(ctx, t, store, "issues")
			if msg := "bd: recheck blocked after " + arm.source(b, c); !doltHasCommitMessage(ctx, t, store, msg) {
				t.Fatalf("%s: the recheck corrected %s but minted no Dolt commit %q", arm.name, c, msg)
			}
		})
	}
}
