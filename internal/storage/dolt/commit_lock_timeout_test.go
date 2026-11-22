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
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestCommitLockTimesOutFailOpen is AC-7: with the lock held longer than the
// store's wait (1s here), the commit waits about that long, then proceeds
// unlocked (fail-open): it returns nil, the Dolt commit landed, the timeout
// was counted on commit_lock_timeouts, and the external holder still holds
// the lock.
func TestCommitLockTimesOutFailOpen(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	store, probe, lockName := commitLockStore(t)
	store.commitLockWait = time.Second
	ctx := context.Background()
	lockTestIssue(t, ctx, store)

	probe.hold(t, lockName)
	holder := probe.connectionID(t)
	before := versioncontrolops.CommitLockTimeouts()

	start := time.Now()
	const msg = "fail-open commit"
	err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		return tx.SetMetadata(ctx, "commit_lock_test", msg)
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunInTransaction under a wedged lock: %v", err)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("commit returned after %v; it did not wait out the commit lock before failing open", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("commit returned after %v; the fail-open wait is not bounded near the configured 1s", elapsed)
	}
	if after := versioncontrolops.CommitLockTimeouts(); after <= before {
		t.Fatalf("commit_lock_timeouts did not increment (before=%d after=%d)", before, after)
	}
	if !probe.logHasMessage(t, msg) {
		t.Fatal("the fail-open commit did not land in dolt_log")
	}
	if got := probe.lockUser(t, lockName); got != holder {
		t.Fatalf("IS_USED_LOCK = %d after the timed-out commit, want the external holder %d: the store must not release a lock it failed to obtain", got, holder)
	}
	probe.release(t, lockName)
}
