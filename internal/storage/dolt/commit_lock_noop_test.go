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
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// TestCommitLockSkippedForNoOpWrite is AC-5: a write that leaves no pending
// change returns well under the commit-lock wait, without issuing GET_LOCK —
// observable as the external holder still holding the lock when it returns.
func TestCommitLockSkippedForNoOpWrite(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	store, probe, lockName := commitLockStore(t)
	ctx := context.Background()
	lockTestIssue(t, ctx, store)

	// Seed the metadata row BEFORE the external holder takes the lock: the
	// seed commit is a real one and must not have to fail open.
	if err := store.RunInTransaction(ctx, "seed metadata value", func(tx storage.Transaction) error {
		return tx.SetMetadata(ctx, "commit_lock_noop", "value")
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	// The premise of the no-op write: with the seed committed, the working
	// set holds nothing committable, so the same-value rewrite below leaves
	// no pending change.
	if tables := probe.dirtyTables(t); len(tables) > 0 {
		t.Fatalf("dolt_status still lists %q after the seed commit; the no-op premise does not hold", tables)
	}

	probe.hold(t, lockName)
	holder := probe.connectionID(t)
	start := time.Now()
	err := store.RunInTransaction(ctx, "no-op write", func(tx storage.Transaction) error {
		// Marks the tracker dirty but writes the same value the row already
		// holds, so dolt_status reports nothing pending when the commit pair
		// is about to start.
		return tx.SetMetadata(ctx, "commit_lock_noop", "value")
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("no-op RunInTransaction: %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("no-op write took %v; it must return well under the commit-lock wait rather than queue on the lock", elapsed)
	}
	if got := probe.lockUser(t, lockName); got != holder {
		t.Fatalf("IS_USED_LOCK = %d after the no-op write returned, want the external holder %d: the store issued GET_LOCK and took the lock (or released the holder's)", got, holder)
	}
	t.Logf("post-noop dolt_status: %q", probe.dirtyTables(t))
	probe.release(t, lockName)
}
