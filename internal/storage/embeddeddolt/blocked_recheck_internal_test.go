//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// embeddedIsBlocked reads the STORED is_blocked flag for id.
//
// It exists because GetIssue cannot answer this question: is_blocked is not in
// the hydrated column list (sqlbuild.IssueBaseColumns), so types.Issue.IsBlocked
// is always false on a hydrated issue and an assertion on it holds no matter
// what the flag says. The read runs with commit=false, so it takes nothing out
// of the recheck scope and triggers no recheck of its own.
func embeddedIsBlocked(t *testing.T, ctx context.Context, store *EmbeddedDoltStore, id string) bool {
	t.Helper()
	var blocked int
	if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&blocked)
	}); err != nil {
		t.Fatalf("read is_blocked for %s: %v", id, err)
	}
	return blocked != 0
}

// TestEmbeddedBlockedRecheckRecordsUnblockingWrites pins, on a real engine,
// that an unblocking write running on the embedded store's own scoped
// transaction records the dependents it recomputed for the post-commit
// recheck: a close, a dependency removal, a delete. The embedded store
// serializes its transactions so the skew of gastownhall/beads#6716 cannot
// occur here; the recording contract is what this tier can verify, and the
// server tier's close_recheck_blocked_test.go races it.
func TestEmbeddedBlockedRecheckRecordsUnblockingWrites(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "recheck", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "rb"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rb-a", "rb-b", "rb-c"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, blocker := range []string{"rb-a", "rb-b"} {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "rb-c", DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dependency rb-c -> %s: %v", blocker, err)
		}
	}

	// record runs one unblocking write in a scoped transaction and returns what
	// it recorded, taking it before the store's own post-commit recheck would.
	record := func(name string, write func(tx *sql.Tx) error) issueops.BlockedRecheck {
		t.Helper()
		var pending issueops.BlockedRecheck
		if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
			if err := write(tx); err != nil {
				return err
			}
			pending = issueops.TakeBlockedRecheck(tx)
			return nil
		}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return pending
	}
	expect := func(name string, pending issueops.BlockedRecheck, source string, excluded ...string) {
		t.Helper()
		if !slices.Contains(pending.IssueIDs, "rb-c") {
			t.Fatalf("%s recorded %v, want the dependent rb-c", name, pending.IssueIDs)
		}
		for _, id := range excluded {
			if slices.Contains(pending.IssueIDs, id) {
				t.Fatalf("%s recorded %v, must never recheck %s", name, pending.IssueIDs, id)
			}
		}
		if want := []string{source}; !slices.Equal(pending.Sources, want) {
			t.Fatalf("%s Sources = %v, want %v", name, pending.Sources, want)
		}
	}

	expect("close", record("close rb-a", func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueInTx(ctx, tx, "rb-a", "done", "tester", "")
		return err
	}), "close of rb-a", "rb-a")

	expect("dependency removal", record("remove rb-c -> rb-b", func(tx *sql.Tx) error {
		_, err := issueops.RemoveDependencyInTx(ctx, tx, "rb-c", "rb-b", "tester", true)
		return err
	}), "dependency removal rb-c -> rb-b")

	// Re-add the edge so the delete has a dependent to record.
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "rb-c", DependsOnID: "rb-b", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("re-add dependency: %v", err)
	}
	expect("delete", record("delete rb-b", func(tx *sql.Tx) error {
		return issueops.DeleteIssueInTx(ctx, tx, "rb-b", "")
	}), "delete of rb-b", "rb-b")

	// The graph ends settled: both blockers gone, rb-c unblocked. Each write
	// above took its pending set out of the transaction, so what settled the
	// flag here is the in-transaction recompute, not the post-commit recheck —
	// that one is pinned by TestEmbeddedBlockedRecheckSettlesWhatTheWriteRecorded.
	if embeddedIsBlocked(t, ctx, store, "rb-c") {
		t.Fatal("rb-c is still blocked after its last blocker was deleted")
	}
}

// TestEmbeddedBlockedRecheckSettlesWhatTheWriteRecorded pins withConn's tail on
// this tier: the recheck must run for whatever the committed transaction
// recorded, with the test taking nothing out of that transaction first. The
// test above takes the pending set inside the body — which is what it is for,
// but it also means the store's tail always rechecks an empty set there, so
// nothing was measuring the tail itself.
//
// Embedded transactions serialize (see recheckBlockedAfterCommit), so the
// snapshot skew of gastownhall/beads#6716 cannot be staged here. The
// transaction instead leaves the residue that skew leaves behind — a dependent
// still flagged blocked once every blocker is closed — by closing the second
// blocker with a raw status write, which no recompute observes. That stands in
// for the concurrent writer this tier cannot host, and from there only a
// post-commit recheck of the recorded dependent can settle the flag.
func TestEmbeddedBlockedRecheckSettlesWhatTheWriteRecorded(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "recheckafter", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "rb"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rb-a", "rb-b", "rb-c"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, blocker := range []string{"rb-a", "rb-b"} {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "rb-c", DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dependency rb-c -> %s: %v", blocker, err)
		}
	}

	var blockedInTx int
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		// Records rb-c and recomputes it: rb-b is open here, so rb-c stays
		// blocked.
		if _, err := issueops.CloseIssueInTx(ctx, tx, "rb-a", "done", "tester", ""); err != nil {
			return err
		}
		// The blocker whose close no recompute sees.
		if _, err := tx.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = ?", "rb-b"); err != nil {
			return err
		}
		// The pending set is deliberately left in the transaction for the
		// store's own tail to take.
		return tx.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", "rb-c").Scan(&blockedInTx)
	}); err != nil {
		t.Fatalf("close rb-a: %v", err)
	}
	if blockedInTx == 0 {
		t.Fatal("rb-c was already unblocked inside the transaction, so this test measures nothing about the post-commit recheck")
	}

	if embeddedIsBlocked(t, ctx, store, "rb-c") {
		t.Fatal("rb-c is still blocked after a committed write recorded it: withConn's tail did not recheck what the transaction recorded")
	}
}

// TestEmbeddedBlockedRecheckFailureKeepsTheWrite pins what a *failing* recheck
// does at the store seam, which is the half of the contract the wrapper's unit
// test cannot reach: the write it follows has already committed, so the store
// call still succeeds and the row is durable, and the failure the store's own
// withConn/commitConn path produces carries issueops.ErrBlockedRecheckFailed
// for the warning that reports it.
func TestEmbeddedBlockedRecheckFailureKeepsTheWrite(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "recheckfail", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "rb"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rb-a", "rb-b", "rb-c"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, blocker := range []string{"rb-a", "rb-b"} {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "rb-c", DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dependency rb-c -> %s: %v", blocker, err)
		}
	}

	// Fail the recheck and nothing else: the write's own commitConn has
	// already passed the read-only check when fn runs, so flipping the flag
	// here leaves the close committed and refuses only the transaction the
	// recheck opens afterwards.
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		if _, err := issueops.CloseIssueInTx(ctx, tx, "rb-a", "done", "tester", ""); err != nil {
			return err
		}
		store.readOnly = true
		return nil
	}); err != nil {
		t.Fatalf("a committed write whose recheck failed returned %v, want nil", err)
	}
	store.readOnly = false

	closed, err := store.GetIssue(ctx, "rb-a")
	if err != nil {
		t.Fatalf("get rb-a: %v", err)
	}
	if closed.Status != types.StatusClosed {
		t.Fatalf("rb-a is %s after a close whose recheck failed, want the write to be durable", closed.Status)
	}

	// The failure itself, on the path that produces it.
	store.readOnly = true
	recheckErr := store.recheckBlockedAfterCommit(ctx, issueops.BlockedRecheck{IssueIDs: []string{"rb-c"}})
	store.readOnly = false
	if !errors.Is(recheckErr, issueops.ErrBlockedRecheckFailed) {
		t.Fatalf("a failed recheck returned %v, want it to carry ErrBlockedRecheckFailed", recheckErr)
	}
}
