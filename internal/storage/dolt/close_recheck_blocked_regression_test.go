package dolt

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestCloseRecheckBlocked_StoreWriteCommittingLastUnblocks is the release-line
// regression test for gastownhall/beads#6716 (formula fan-in stall). It uses
// only store entry points that exist before the fix, so it compiles on both
// sides of it and fails without it.
//
// A close runs through the store's own write runner and pins its snapshot
// while both blockers are open. A sibling close then commits on another
// connection; its in-transaction recompute cannot see the store's uncommitted
// close, so it leaves rb-c blocked. The store's close commits last, and its
// in-transaction recompute likewise saw the sibling open. Without the
// post-commit recheck rb-c stays is_blocked=1 and missing from ready work on
// every committed snapshot.
func TestCloseRecheckBlocked_StoreWriteCommittingLastUnblocks(t *testing.T) {
	runners := []struct {
		name string
		run  func(*DoltStore, context.Context, func(*sql.Tx) error) error
	}{
		{"withWriteTx", (*DoltStore).withWriteTx},
		{"withRetryTx", (*DoltStore).withRetryTx},
	}
	for _, runner := range runners {
		t.Run(runner.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			for _, id := range []string{"rb-a", "rb-b", "rb-c"} {
				createPerm(t, ctx, store, id)
			}
			addDependency(t, ctx, store, "rb-c", "rb-a", types.DepBlocks)
			addDependency(t, ctx, store, "rb-c", "rb-b", types.DepBlocks)
			assertIsBlocked(t, ctx, store, "issues", "rb-c", true)

			siblingCommitted := false
			if err := runner.run(store, ctx, func(tx *sql.Tx) error {
				var open int
				if err := tx.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM issues WHERE id IN ('rb-a', 'rb-b') AND status <> 'closed'").Scan(&open); err != nil {
					return err
				}
				if open != 2 {
					t.Fatalf("store close snapshot sees %d open blockers, want 2", open)
				}
				if _, err := issueops.CloseIssueInTx(ctx, tx, "rb-b", "done", "tester", ""); err != nil {
					return err
				}
				if siblingCommitted {
					return nil
				}
				closeSiblingOnOwnConnection(t, ctx, store, "rb-a")
				siblingCommitted = true
				return nil
			}); err != nil {
				t.Fatalf("store close of rb-b: %v", err)
			}

			assertIsBlocked(t, ctx, store, "issues", "rb-c", false)
			ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if !slices.ContainsFunc(ready, func(i *types.Issue) bool { return i.ID == "rb-c" }) {
				t.Fatal("rb-c has no open blockers but is missing from ready work")
			}
		})
	}
}

// closeSiblingOnOwnConnection closes id in a transaction on a second pooled
// connection and commits it, with the ordinary in-transaction recompute, and
// checks that it left rb-c blocked: the stale state #6716 starts from.
func closeSiblingOnOwnConnection(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire sibling connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin sibling tx: %v", err)
	}
	if _, err := issueops.CloseIssueInTx(ctx, tx, id, "done", "tester", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("sibling close of %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit sibling close of %s: %v", id, err)
	}
	// Read on this connection: the store's runner holds the pool's other one.
	var blocked bool
	if err := conn.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = 'rb-c'").Scan(&blocked); err != nil {
		t.Fatalf("read rb-c after sibling close: %v", err)
	}
	if !blocked {
		t.Fatal("sibling close unblocked rb-c although the store's close of the other blocker has not committed")
	}
}
