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
