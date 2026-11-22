package dolt

// Commit-lock tests (GH#8590, dolthub/dolt#12258): every server-mode commit
// plane holds the per-database named lock (versioncontrolops.CommitLockName)
// across its DOLT_ADD/DOLT_COMMIT pair, takes it after the pending fast path
// so a no-op write pays no lock round trips, and releases it on every return
// path. Each test holds the lock from a second session and asserts the commit
// does not land until the holder releases it — the shape that, before the
// lock existed, produced Dolt commits carrying merge conflict artifacts under
// concurrent writers.
import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestCommitLockReleasedOnEveryReturnPath is AC-4: each return path of a
// server-mode commit — success, nothing staged, a failed DOLT_ADD, a failed
// DOLT_COMMIT, the funnel's screen refusal, and a context cancelled after an
// in-tx lock grant — leaves the lock free as soon as the call returns.
func TestCommitLockReleasedOnEveryReturnPath(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	t.Run("Success", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		if err := store.AddLabel(ctx, id, "lock-release-1", "tester"); err != nil {
			t.Fatalf("AddLabel: %v", err)
		}
		probe.requireLockFree(t, lockName, "successful publish")
	})

	t.Run("NothingStaged", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		// A RunInTransaction whose body writes nothing has no pending change
		// and must return before ever taking the lock.
		start := time.Now()
		err := store.RunInTransaction(ctx, "nothing to commit", func(tx storage.Transaction) error { return nil })
		if err != nil {
			t.Fatalf("RunInTransaction: %v", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("no-op RunInTransaction took %v; it should not have waited on the commit lock", elapsed)
		}
		probe.requireLockFree(t, lockName, "nothing-staged skip")
	})

	t.Run("FailedDoltAdd", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		// Out-of-band dirt — a real row change — so the pending fast path
		// does not skip staging before the failing statement under test.
		if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'lock-test dirty' WHERE id = ?", id); err != nil {
			t.Fatalf("dirty working set: %v", err)
		}
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin conn: %v", err)
		}
		defer conn.Close()
		err = versioncontrolops.StageAndCommit(ctx, conn, map[string]bool{"no_such_table_lock": true}, "must fail dolt add", "Test Author <t@example.com>")
		if err == nil || !strings.Contains(err.Error(), "dolt add") {
			t.Fatalf("StageAndCommit with an unknown table should fail at DOLT_ADD, got %v", err)
		}
		probe.requireLockFree(t, lockName, "failed DOLT_ADD")
	})

	t.Run("FailedDoltCommit", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'lock-test dirty' WHERE id = ?", id); err != nil {
			t.Fatalf("dirty working set: %v", err)
		}
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin conn: %v", err)
		}
		defer conn.Close()
		// Dolt rejects a --author that is not "Name <email>"; the commit
		// statement fails after the lock was taken and rows were staged.
		err = versioncontrolops.StageAndCommit(ctx, conn, map[string]bool{"issues": true}, "must fail dolt commit", "not an address")
		if err == nil || !strings.Contains(err.Error(), "dolt commit") {
			t.Fatalf("StageAndCommit with a malformed author should fail at DOLT_COMMIT, got %v", err)
		}
		probe.requireLockFree(t, lockName, "failed DOLT_COMMIT")
	})

	t.Run("FunnelScreenRefusal", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		ctx := context.Background()
		id := lockTestIssue(t, ctx, store)
		// Out-of-band dirt — a real row change — so the funnel stages
		// something real before its post-stage screen refuses: the refusal
		// path that runs INSIDE the lock (unstageRefused's DOLT_RESET and
		// the deferred release).
		if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'screen-test dirty' WHERE id = ?", id); err != nil {
			t.Fatalf("dirty working set: %v", err)
		}
		refusal := errors.New("staged set refused by test screen")
		err := store.stageAndCommit(ctx, []string{"issues"}, "screened commit", stageOptions{
			screen: func(ctx context.Context, conn *sql.Conn, staged bool) error {
				if staged {
					return refusal
				}
				return nil
			},
		})
		if !errors.Is(err, refusal) {
			t.Fatalf("funnel screen refusal: got %v, want the test screen's refusal", err)
		}
		probe.requireLockFree(t, lockName, "funnel screen refusal")
	})

	t.Run("ContextCancelledAfterInTxGrant", func(t *testing.T) {
		store, probe, lockName := commitLockStore(t)
		id := lockTestIssue(t, context.Background(), store)
		ctx, cancel := context.WithCancel(context.Background())
		// One write transaction whose body takes the in-tx lock, then loses
		// its context before committing — the deferred release must run while
		// the transaction is still open, on the tx's own session.
		_, txErr := store.commitWriteTxOn(ctx, store.db, func(tx *sql.Tx) error {
			release, herr := store.holdCommitLockInTx(ctx, tx)
			if herr != nil {
				return herr
			}
			defer release()
			if got := probe.lockUser(t, lockName); got == 0 {
				t.Fatal("in-tx commit lock not held after a successful grant")
			}
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET title = title WHERE id = ?", id); err != nil {
				return err
			}
			cancel()
			return nil
		})
		if txErr == nil {
			t.Fatal("cancelled-context write transaction should have failed to commit")
		}
		probe.requireLockFree(t, lockName, "context cancelled after an in-tx grant")
		// The settlement must not have discarded a healthy pinned session
		// into the pool: the store still works for a full write cycle.
		if err := store.AddLabel(context.Background(), id, "lock-release-2", "tester"); err != nil {
			t.Fatalf("AddLabel after cancelled in-tx lock: %v", err)
		}
		probe.requireLockFree(t, lockName, "post-cancel write cycle")
	})
}
