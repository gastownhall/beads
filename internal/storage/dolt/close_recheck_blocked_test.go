package dolt

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// seedSiblingBlockers creates rb-a and rb-b, both blocking rb-c.
func seedSiblingBlockers(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	for _, id := range []string{"rb-a", "rb-b", "rb-c"} {
		createPerm(t, ctx, store, id)
	}
	addDependency(t, ctx, store, "rb-c", "rb-a", types.DepBlocks)
	addDependency(t, ctx, store, "rb-c", "rb-b", types.DepBlocks)
	assertIsBlocked(t, ctx, store, "issues", "rb-c", true)
}

// beginRacingWrite opens a transaction on its own pooled connection, pins its
// snapshot by reading both blockers, and runs one unblocking write inside it
// with the ordinary in-transaction recompute, exactly as the store's write
// paths do. It returns the dependents the write recorded for the post-commit
// recheck and a commit step that lands the write and frees the connection.
func beginRacingWrite(t *testing.T, ctx context.Context, store *DoltStore, name string, write func(tx *sql.Tx) error) (pending issueops.BlockedRecheck, commit func()) {
	t.Helper()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection for %s: %v", name, err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx for %s: %v", name, err)
	}
	clearScope := issueops.ScopeBlockedRecheckTransaction(tx)
	defer clearScope()
	var open int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id IN ('rb-a', 'rb-b') AND status <> 'closed'").Scan(&open); err != nil {
		t.Fatalf("pin snapshot for %s: %v", name, err)
	}
	if open != 2 {
		t.Fatalf("snapshot for %s sees %d open blockers, want 2", name, open)
	}
	if err := write(tx); err != nil {
		t.Fatalf("%s in tx: %v", name, err)
	}
	return issueops.TakeBlockedRecheck(tx), func() {
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", name, err)
		}
		_ = conn.Close()
	}
}

// beginRacingClose is beginRacingWrite for a close of id.
func beginRacingClose(t *testing.T, ctx context.Context, store *DoltStore, id string) (pending issueops.BlockedRecheck, commit func()) {
	t.Helper()
	return beginRacingWrite(t, ctx, store, "close of "+id, func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueInTx(ctx, tx, id, "done", "tester", "")
		return err
	})
}

// TestCloseRecheckBlocked_ConcurrentSiblingCloses is gastownhall/beads#6716:
// two closes of sibling blockers whose transactions both began before either
// committed each see the other blocker open, so neither writes the dependent's
// row and both commit without a conflicting cell. The in-transaction recompute
// cannot see the other close; the post-commit recheck over the same ids can.
func TestCloseRecheckBlocked_ConcurrentSiblingCloses(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedSiblingBlockers(t, ctx, store)

	pendingA, commitA := beginRacingClose(t, ctx, store, "rb-a")
	pendingB, commitB := beginRacingClose(t, ctx, store, "rb-b")
	for _, pending := range []issueops.BlockedRecheck{pendingA, pendingB} {
		if !slices.Contains(pending.IssueIDs, "rb-c") || slices.Contains(pending.IssueIDs, "rb-a") || slices.Contains(pending.IssueIDs, "rb-b") {
			t.Fatalf("recorded recheck ids = %v, want the dependent rb-c and never the closed issue", pending.IssueIDs)
		}
	}

	commitA()
	assertIsBlocked(t, ctx, store, "issues", "rb-c", true)
	if err := store.recheckBlockedAfterCommit(ctx, pendingA); err != nil {
		t.Fatalf("recheck after closing rb-a: %v", err)
	}
	// rb-b is still open on every committed snapshot: rb-c stays blocked.
	assertIsBlocked(t, ctx, store, "issues", "rb-c", true)

	commitB()
	// The stale state the in-transaction recompute leaves behind.
	assertIsBlocked(t, ctx, store, "issues", "rb-c", true)
	if err := store.recheckBlockedAfterCommit(ctx, pendingB); err != nil {
		t.Fatalf("recheck after closing rb-b: %v", err)
	}
	assertIsBlocked(t, ctx, store, "issues", "rb-c", false)
	requireCleanTables(ctx, t, store, "issues")
	if !doltHasCommitMessage(ctx, t, store, "bd: recheck blocked after close of rb-b") {
		t.Fatal("the recheck corrected rb-c but minted no Dolt commit for it")
	}
}

// TestCloseRecheckBlocked_PublicClosePaths covers the store methods that own
// the recheck: a close with dependents leaves the dependent settled and the
// working set clean, and a close with no dependents records nothing to recheck.
func TestCloseRecheckBlocked_PublicClosePaths(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedSiblingBlockers(t, ctx, store)
	createPerm(t, ctx, store, "rb-lone")

	if err := store.CloseIssue(ctx, "rb-a", "done", "tester", ""); err != nil {
		t.Fatalf("close rb-a: %v", err)
	}
	assertIsBlocked(t, ctx, store, "issues", "rb-c", true)
	if err := store.UpdateIssue(ctx, "rb-b", map[string]interface{}{"status": types.StatusClosed}, "tester"); err != nil {
		t.Fatalf("close rb-b through update: %v", err)
	}
	assertIsBlocked(t, ctx, store, "issues", "rb-c", false)
	requireCleanTables(ctx, t, store, "issues")

	var pending issueops.BlockedRecheck
	if err := store.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := issueops.CloseIssueInTx(ctx, tx, "rb-lone", "done", "tester", ""); err != nil {
			return err
		}
		pending = issueops.TakeBlockedRecheck(tx)
		return nil
	}); err != nil {
		t.Fatalf("close rb-lone: %v", err)
	}
	if !pending.Empty() {
		t.Fatalf("close of an issue with no dependents recorded %+v to recheck, want nothing", pending)
	}
}

// TestCloseRecheckBlocked_ConcurrentCloseAndDependencyRemoval is #6716 with a
// dependency removal as the second writer: a close of rb-a racing a removal of
// the rb-c -> rb-b edge. Each transaction still sees the other blocker in
// place, neither writes rb-c, and the one that commits last leaves rb-c stale
// unless its own recorded recheck settles it.
func TestCloseRecheckBlocked_ConcurrentCloseAndDependencyRemoval(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedSiblingBlockers(t, ctx, store)

	pendingClose, commitClose := beginRacingClose(t, ctx, store, "rb-a")
	pendingRemove, commitRemove := beginRacingWrite(t, ctx, store, "dependency removal rb-c -> rb-b", func(tx *sql.Tx) error {
		_, err := issueops.RemoveDependencyInTx(ctx, tx, "rb-c", "rb-b", "tester", true)
		return err
	})
	if !slices.Contains(pendingRemove.IssueIDs, "rb-c") {
		t.Fatalf("dependency removal recorded %v, want the dependent rb-c", pendingRemove.IssueIDs)
	}
	if want := []string{"dependency removal rb-c -> rb-b"}; !slices.Equal(pendingRemove.Sources, want) {
		t.Fatalf("dependency removal Sources = %v, want %v", pendingRemove.Sources, want)
	}

	commitClose()
	if err := store.recheckBlockedAfterCommit(ctx, pendingClose); err != nil {
		t.Fatalf("recheck after closing rb-a: %v", err)
	}
	// The rb-c -> rb-b edge is still present on every committed snapshot.
	assertIsBlocked(t, ctx, store, "issues", "rb-c", true)

	commitRemove()
	// The stale state the in-transaction recompute leaves behind.
	assertIsBlocked(t, ctx, store, "issues", "rb-c", true)
	if err := store.recheckBlockedAfterCommit(ctx, pendingRemove); err != nil {
		t.Fatalf("recheck after removing rb-c -> rb-b: %v", err)
	}
	assertIsBlocked(t, ctx, store, "issues", "rb-c", false)
	requireCleanTables(ctx, t, store, "issues")
	if !doltHasCommitMessage(ctx, t, store, "bd: recheck blocked after dependency removal rb-c -> rb-b") {
		t.Fatal("the recheck corrected rb-c but minted no Dolt commit for it")
	}
}

// TestCloseRecheckBlocked_DeleteRecordsDependents: a delete of a blocker
// records its dependents for the recheck and never the deleted row itself.
func TestCloseRecheckBlocked_DeleteRecordsDependents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedSiblingBlockers(t, ctx, store)

	var pending issueops.BlockedRecheck
	if err := store.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.DeleteIssueInTx(ctx, tx, "rb-b", "tester"); err != nil {
			return err
		}
		pending = issueops.TakeBlockedRecheck(tx)
		return nil
	}); err != nil {
		t.Fatalf("delete rb-b: %v", err)
	}
	if !slices.Contains(pending.IssueIDs, "rb-c") || slices.Contains(pending.IssueIDs, "rb-b") {
		t.Fatalf("delete recorded %v, want the dependent rb-c and never the deleted rb-b", pending.IssueIDs)
	}
	if want := []string{"delete of rb-b"}; !slices.Equal(pending.Sources, want) {
		t.Fatalf("delete Sources = %v, want %v", pending.Sources, want)
	}
}

// TestCloseRecheckBlocked_FailureKeepsTheWrite pins the swallow on this tier —
// the one #6716 was reported on, and the one every other test here reaches by
// calling recheckBlockedAfterCommit directly, which crosses neither runner's
// tail. Both runners log a recheck failure and return nil, so a close whose
// recheck failed is durable and its caller is never told to retry a write that
// landed; restoring `return s.recheckBlockedAfterCommit(...)` on either would
// otherwise turn nothing red. It mirrors the embedded tier's
// TestEmbeddedBlockedRecheckFailureKeepsTheWrite.
func TestCloseRecheckBlocked_FailureKeepsTheWrite(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedSiblingBlockers(t, ctx, store)

	// Fail the recheck and nothing else, the way the embedded test flips
	// readOnly: commitWriteTx has already passed its closed check by the time fn
	// runs, so marking the store here leaves this close committed and refuses
	// only the transaction the recheck opens next. ErrStoreClosed is not
	// retryable, so that refusal is immediate and never reaches the breaker.
	closeInTx := func(id string) func(tx *sql.Tx) error {
		return func(tx *sql.Tx) error {
			if _, err := issueops.CloseIssueInTx(ctx, tx, id, "done", "tester", ""); err != nil {
				return err
			}
			store.closed.Store(true)
			return nil
		}
	}

	if err := store.withRetryTx(ctx, closeInTx("rb-a")); err != nil {
		store.closed.Store(false)
		t.Fatalf("withRetryTx: a committed close whose recheck failed returned %v, want nil", err)
	}
	store.closed.Store(false)

	if err := store.withWriteTx(ctx, closeInTx("rb-b")); err != nil {
		store.closed.Store(false)
		t.Fatalf("withWriteTx: a committed close whose recheck failed returned %v, want nil", err)
	}
	store.closed.Store(false)

	for _, id := range []string{"rb-a", "rb-b"} {
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.Status != types.StatusClosed {
			t.Fatalf("%s is %s after a close whose recheck failed, want the write to be durable", id, got.Status)
		}
	}

	// The failure the runners swallow is the one that names itself, so the
	// warning they log tells a stale dependent from a write that never landed.
	store.closed.Store(true)
	recheckErr := store.recheckBlockedAfterCommit(ctx, issueops.BlockedRecheck{IssueIDs: []string{"rb-c"}})
	store.closed.Store(false)
	if !errors.Is(recheckErr, issueops.ErrBlockedRecheckFailed) {
		t.Fatalf("a failed recheck returned %v, want it to carry ErrBlockedRecheckFailed", recheckErr)
	}
}
