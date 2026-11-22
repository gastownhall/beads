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
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestCommitLockIsPerDatabase is AC-6: a lock held under another database's
// name on the same server must not delay this database's commit.
func TestCommitLockIsPerDatabase(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	store, probe, _ := commitLockStore(t)
	ctx := context.Background()
	id := lockTestIssue(t, ctx, store)

	otherDB := versioncontrolops.CommitLockName("other_db")
	probe.hold(t, otherDB)
	otherHolder := probe.connectionID(t)
	start := time.Now()
	if err := store.AddLabel(ctx, id, "cross-db-1", "tester"); err != nil {
		t.Fatalf("AddLabel while another database's lock is held: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("commit took %v with only an unrelated database's lock held: the lock name is not per-database", elapsed)
	}
	// The unrelated database's lock is untouched by this store's commit: the
	// probe still holds it. A commit lock that was not per-database would
	// have made the store queue 5s behind this hold (or stolen it).
	if got := probe.lockUser(t, otherDB); got != otherHolder {
		t.Fatalf("IS_USED_LOCK(%q) = %d after the commit, want the probe %d", otherDB, got, otherHolder)
	}
	probe.release(t, otherDB)

	// The same name shape for this store's database DOES block it (checked by
	// AC-1); pin here only that the name this store derives is exactly the
	// documented, stable one, since a shape change would silently undo every
	// plane's serialization while unrelated tests kept passing.
	if got, want := versioncontrolops.CommitLockName(store.database), "bd_commit:"+strings.ToLower(store.database); got != want {
		t.Fatalf("CommitLockName(%q) = %q, want %q", store.database, got, want)
	}
}
