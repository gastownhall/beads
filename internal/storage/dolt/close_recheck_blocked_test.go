package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil/sqlgate"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// seedSiblingBlockers creates rb-a and rb-b, both blocking rb-c.
func seedSiblingBlockers(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	seedSiblingBlockersAs(t, ctx, store, "rb-a", "rb-b", "rb-c")
}

// seedSiblingBlockersAs is seedSiblingBlockers for a named triple, so one store
// can carry more than one independent fixture.
func seedSiblingBlockersAs(t *testing.T, ctx context.Context, store *DoltStore, blockerA, blockerB, dependent string) {
	t.Helper()
	for _, id := range []string{blockerA, blockerB, dependent} {
		createPerm(t, ctx, store, id)
	}
	addDependency(t, ctx, store, dependent, blockerA, types.DepBlocks)
	addDependency(t, ctx, store, dependent, blockerB, types.DepBlocks)
	assertIsBlocked(t, ctx, store, "issues", dependent, true)
}

// beginRacingWrite opens a transaction on its own pooled connection, pins its
// snapshot by reading both blockers, and runs one unblocking write inside it
// with the ordinary in-transaction recompute, exactly as the store's write
// paths do. It returns the dependents the write recorded for the post-commit
// recheck and a commit step that lands the write and frees the connection.
func beginRacingWrite(t *testing.T, ctx context.Context, store *DoltStore, name string, write func(tx *sql.Tx) error) (pending issueops.BlockedRecheck, commit func()) {
	t.Helper()
	return beginRacingWriteOn(t, ctx, store, name, []string{"rb-a", "rb-b"}, write)
}

// beginRacingWriteOn is beginRacingWrite for a named fixture: blockers are the
// ids whose open state pins the snapshot.
func beginRacingWriteOn(t *testing.T, ctx context.Context, store *DoltStore, name string, blockers []string, write func(tx *sql.Tx) error) (pending issueops.BlockedRecheck, commit func()) {
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
	placeholders := make([]string, 0, len(blockers))
	args := make([]any, 0, len(blockers))
	for _, id := range blockers {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	var open int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id IN ("+strings.Join(placeholders, ", ")+") AND status <> 'closed'",
		args...).Scan(&open); err != nil {
		t.Fatalf("pin snapshot for %s: %v", name, err)
	}
	if open != len(blockers) {
		t.Fatalf("snapshot for %s sees %d open blockers, want %d", name, open, len(blockers))
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

// TestCloseRecheckBlocked_IssueOperationCommitsBeforeRecheck is
// gastownhall/beads#7035. An operation on the issue-operation runner mints its
// own Dolt commit after its SQL transaction, and that commit's message carries
// the operation's provenance. The recheck's commit stages the whole issues
// table, so when a race made the recheck change a row and the recheck ran
// first, the close's row went out under the recheck's message and the
// operation's commit had nothing left to stage: dolt_log never showed the
// operation. The operation's commit comes first, then the recheck's.
func TestCloseRecheckBlocked_IssueOperationCommitsBeforeRecheck(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedSiblingBlockers(t, ctx, store)

	// rb-a closes on its own connection and lands inside the operation's
	// window: after the operation pinned its snapshot, before it commits.
	// Neither close sees the other, so only the recheck unblocks rb-c.
	_, commitA := beginRacingClose(t, ctx, store, "rb-a")
	var landA sync.Once
	attempts := 0
	const operationMsg = "bd: close rb-b on the issue-operation runner"
	const recheckMsg = "bd: recheck blocked after close of rb-b"
	err := store.runIssueOperationTx(ctx, operationMsg, func(tx *sql.Tx) (issueops.ChangedTables, error) {
		attempts++
		var open int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issues WHERE id IN ('rb-a', 'rb-b') AND status <> 'closed'").Scan(&open); err != nil {
			return nil, err
		}
		if attempts == 1 && open != 2 {
			return nil, fmt.Errorf("the operation's snapshot sees %d open blockers, want 2", open)
		}
		landA.Do(commitA)
		if _, err := issueops.CloseIssueInTx(ctx, tx, "rb-b", "done", "tester", ""); err != nil {
			return nil, err
		}
		tables := issueops.ChangedTables{}
		tables.Add("issues", "events")
		return tables, nil
	})
	if err != nil {
		t.Fatalf("close rb-b on the issue-operation runner: %v", err)
	}
	if attempts != 1 {
		// A replayed body sees rb-a closed and unblocks rb-c itself, so the
		// recheck would have nothing to publish and the race is not the one
		// under test.
		t.Fatalf("the operation body ran %d times, want 1: the closes did not race", attempts)
	}

	assertIsBlocked(t, ctx, store, "issues", "rb-c", false)
	requireCleanTables(ctx, t, store, "issues")
	rows, err := store.db.QueryContext(ctx, "SELECT message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	// dolt_log lists the newest commit first.
	operationAt, recheckAt := -1, -1
	for i := 0; rows.Next(); i++ {
		var message string
		if err := rows.Scan(&message); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		switch message {
		case operationMsg:
			operationAt = i
		case recheckMsg:
			recheckAt = i
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}
	if operationAt < 0 {
		t.Fatalf("dolt_log has no %q commit: the recheck published the operation's rows under its own message", operationMsg)
	}
	if recheckAt < 0 {
		t.Fatalf("dolt_log has no %q commit: the recheck corrected rb-c but minted no Dolt commit for it", recheckMsg)
	}
	if recheckAt > operationAt {
		t.Fatalf("the recheck's commit is older than the operation's (log positions %d and %d, newest first), want the operation's commit first", recheckAt, operationAt)
	}
}

// doltLogEntry is one dolt_log row.
type doltLogEntry struct{ hash, message string }

// readDoltLog returns dolt_log, newest commit first.
func readDoltLog(ctx context.Context, t *testing.T, store *DoltStore) []doltLogEntry {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	var log []doltLogEntry
	for rows.Next() {
		var entry doltLogEntry
		if err := rows.Scan(&entry.hash, &entry.message); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		log = append(log, entry)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}
	return log
}

// doltLogIndex returns the position of the newest commit whose message
// matches, or -1.
func doltLogIndex(log []doltLogEntry, match func(message string) bool) int {
	for i, entry := range log {
		if match(entry.message) {
			return i
		}
	}
	return -1
}

// requireOperationCommitBeforeRecheck fails unless dolt_log holds the
// operation's own commit and, newer than it, the recheck's commit.
func requireOperationCommitBeforeRecheck(t *testing.T, log []doltLogEntry, operationMsg, recheckMsg string) {
	t.Helper()
	operationAt := doltLogIndex(log, func(m string) bool { return m == operationMsg })
	recheckAt := doltLogIndex(log, func(m string) bool { return m == recheckMsg })
	if recheckAt < 0 {
		t.Fatalf("dolt_log has no %q commit: the race under test was not reproduced; log: %v", recheckMsg, log)
	}
	if operationAt < 0 {
		t.Fatalf("dolt_log has no %q commit: the recheck published the operation's rows under its own message; log: %v",
			operationMsg, log)
	}
	if recheckAt > operationAt {
		t.Fatalf("the recheck's commit is older than %q (log positions %d and %d, newest first), want the operation's commit first; log: %v",
			operationMsg, recheckAt, operationAt, log)
	}
}

// TestCloseRecheckBlocked_LifecycleTransactionCommitsBeforeRecheck is
// gastownhall/beads#7035 on RunInIssueLifecycleTransaction, the tracker-sync
// path. The transition mints its Dolt commit after its SQL transaction, as the
// issue-operation runner does, and must settle the recheck after that commit.
// Settled first, the recheck's commit staged the whole issues table, the
// close of rb-b with it, and the transition's own commit was left with
// nothing to stage (events is dolt-ignored): dolt_log never showed it.
func TestCloseRecheckBlocked_LifecycleTransactionCommitsBeforeRecheck(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedSiblingBlockers(t, ctx, store)

	// rb-a closes on its own connection inside the transition's window: after
	// the transition pinned its snapshot, before it commits. Neither close sees
	// the other, so only the recheck unblocks rb-c.
	_, commitA := beginRacingClose(t, ctx, store, "rb-a")
	var landA sync.Once
	calls := 0
	const operationMsg = "bd: tracker update rb-b"
	err := store.RunInIssueLifecycleTransaction(ctx, operationMsg, func(tx storage.IssueLifecycleTransaction) error {
		calls++
		for _, id := range []string{"rb-a", "rb-b"} {
			issue, err := tx.GetIssue(ctx, id)
			if err != nil {
				return err
			}
			if issue.Status == types.StatusClosed {
				return fmt.Errorf("the transition's snapshot sees %s closed, want both blockers open", id)
			}
		}
		landA.Do(commitA)
		return tx.CloseIssue(ctx, "rb-b", "done", "tester", "")
	})
	if err != nil {
		t.Fatalf("close rb-b in a lifecycle transaction: %v", err)
	}
	if calls != 1 {
		t.Fatalf("lifecycle callback ran %d times, want 1", calls)
	}

	assertIsBlocked(t, ctx, store, "issues", "rb-c", false)
	requireCleanTables(ctx, t, store, "issues")
	requireOperationCommitBeforeRecheck(t, readDoltLog(ctx, t, store), operationMsg, "bd: recheck blocked after close of rb-b")
}

// TestCloseRecheckBlocked_DeletesCommitBeforeRecheck is gastownhall/beads#7035
// on the deletes that mint their Dolt commit after their SQL transaction:
// DeleteIssue, DeleteIssues and the role Deleter, unguarded and fenced. Each
// case seeds its own blockers a and b of c, deletes b while held at its SQL
// commit point, and closes a to completion in that window, so neither write
// sees the other and only the delete's post-commit recheck unblocks c. The
// recheck's commit stages the whole issues table: settled before the delete's
// own commit, it published b's deletion while the dependencies row naming b
// stayed in the published root, a commit that breaks fk_dep_issue_target for
// any push, pull or AS OF read that lands on it. The delete's commit comes
// first, and no commit the race published has a dependencies row naming a
// missing b.
func TestCloseRecheckBlocked_DeletesCommitBeforeRecheck(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	gate := raceGatedStore(t, store)

	roleDelete := func(ctx context.Context, req publicops.DeleteRequest) error {
		deleter, err := store.Deleter()
		if err != nil {
			return err
		}
		res, err := deleter.Delete(ctx, req)
		if err == nil && res.Deleted != 1 {
			return fmt.Errorf("deleted %d rows, want 1", res.Deleted)
		}
		return err
	}
	cases := []struct {
		name string
		// message is the delete's own commit message for id.
		message func(id string) string
		// deleteOf runs before the race and returns the delete of id to
		// hold. c depends on id, so the batch and role deletes force.
		deleteOf func(ctx context.Context, t *testing.T, id string) func(context.Context) error
	}{
		{
			name:    "DeleteIssue",
			message: func(id string) string { return "bd: delete " + id },
			deleteOf: func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
				return func(ctx context.Context) error { return store.DeleteIssue(ctx, id) }
			},
		},
		{
			name:    "DeleteIssues",
			message: func(string) string { return "bd: delete 1 issue(s)" },
			deleteOf: func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
				return func(ctx context.Context) error {
					res, err := store.DeleteIssues(ctx, []string{id}, false, true, false)
					if err == nil && res.DeletedCount != 1 {
						return fmt.Errorf("deleted %d issues, want 1", res.DeletedCount)
					}
					return err
				}
			},
		},
		{
			name:    "Deleter",
			message: func(string) string { return "bd: delete 1 issue(s)" },
			deleteOf: func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
				return func(ctx context.Context) error {
					return roleDelete(ctx, publicops.DeleteRequest{Actor: "tester", IDs: []string{id}, Force: true})
				}
			},
		},
		{
			// An ExpectedVersion on a server-mode store runs the write on
			// withDeleteFence's pinned connection.
			name:    "DeleterFenced",
			message: func(string) string { return "bd: delete 1 issue(s)" },
			deleteOf: func(ctx context.Context, t *testing.T, id string) func(context.Context) error {
				if !store.serverMode {
					t.Fatal("the fenced delete runs only on a server-mode store")
				}
				issue, err := store.GetIssue(ctx, id)
				if err != nil {
					t.Fatalf("read %s's version: %v", id, err)
				}
				version := issue.RowVersion
				return func(ctx context.Context) error {
					return roleDelete(ctx, publicops.DeleteRequest{
						Actor: "tester", IDs: []string{id}, Force: true, ExpectedVersion: &version,
					})
				}
			},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()
			a, b, c := fmt.Sprintf("rd%d-a", i), fmt.Sprintf("rd%d-b", i), fmt.Sprintf("rd%d-c", i)
			seedSiblingBlockersAs(t, ctx, store, a, b, c)
			del := tc.deleteOf(ctx, t, b)

			label := t.Name()
			hold := gate.Arm(label)
			defer hold.Release()
			done := make(chan error, 1)
			go func() { done <- del(sqlgate.WithLabel(ctx, label)) }()
			select {
			case <-hold.Reached():
			case err := <-done:
				t.Fatalf("the delete finished before its commit point: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("the delete never reached its commit point")
			}
			if err := store.CloseIssue(ctx, a, "done", "tester", ""); err != nil {
				t.Fatalf("close %s while the delete is held: %v", a, err)
			}
			// b is still open on every committed snapshot: c stays blocked.
			assertIsBlocked(t, ctx, store, "issues", c, true)
			hold.Release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("delete %s: %v", b, err)
				}
			case <-time.After(60 * time.Second):
				t.Fatal("the delete did not finish after its release")
			}
			if kind := gate.CommitKind(label); kind != "Tx.Commit" {
				t.Fatalf("the delete was held at %q, want its SQL Tx.Commit", kind)
			}

			assertIsBlocked(t, ctx, store, "issues", c, false)
			requireCleanTables(ctx, t, store, "issues", "dependencies")
			log := readDoltLog(ctx, t, store)
			closeAt := doltLogIndex(log, func(m string) bool { return m == "bd: close "+a })
			if closeAt < 0 {
				t.Fatalf("dolt_log has no %q commit; log: %v", "bd: close "+a, log)
			}
			// The cases share the store: only the commits newer than this
			// case's close of a are this race's.
			race := log[:closeAt]
			count := func(query, hash string) int {
				t.Helper()
				var n int
				if err := store.db.QueryRowContext(ctx, fmt.Sprintf(query, hash), b).Scan(&n); err != nil {
					t.Fatalf("count at %s: %v", hash, err)
				}
				return n
			}
			for _, entry := range race {
				issues := count("SELECT COUNT(*) FROM issues AS OF '%s' WHERE id = ?", entry.hash)
				deps := count("SELECT COUNT(*) FROM dependencies AS OF '%s' WHERE depends_on_issue_id = ?", entry.hash)
				if issues == 0 && deps > 0 {
					t.Errorf("published commit %s %q has no %s issues row but %d dependencies row(s) naming it",
						entry.hash, entry.message, b, deps)
				}
			}
			requireOperationCommitBeforeRecheck(t, race, tc.message(b), "bd: recheck blocked after delete of "+b)
		})
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

// TestCloseRecheckBlocked_RunnerTailRechecksACommittedWrite pins the wiring
// this feature IS: a write that commits on a store write runner gets its
// recorded dependents rechecked by that runner, with no help from the test.
//
// Every other test in this file reaches the recheck another way — it calls
// recheckBlockedAfterCommit itself, or takes the pending set out of the
// transaction before the runner can — so all of them stay green when both
// runner tails are deleted, and the tier #6716 was reported on had no
// regression pin on its own deliverable.
//
// Each arm makes the in-transaction recompute provably blind rather than
// hoping for an interleaving: a racing close is held open so the runner's
// transaction cannot see it, the arm reads the dependent's flag inside that
// transaction to record what its own recompute concluded, and the racing close
// is landed from inside the runner's body — before the runner commits, after
// its snapshot was taken. Only a recheck on a fresh snapshot can settle the
// dependent from there. withRetryTx and withWriteTx carry separate tail calls,
// so each gets its own arm and its own fixture.
func TestCloseRecheckBlocked_RunnerTailRechecksACommittedWrite(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, arm := range []struct {
		runner    string
		run       func(context.Context, func(*sql.Tx) error) error
		racing    string // closed by the transaction the runner cannot see
		committed string // closed through the runner
		dependent string
	}{
		{"withRetryTx", store.withRetryTx, "rt-a", "rt-b", "rt-c"},
		{"withWriteTx", store.withWriteTx, "wt-a", "wt-b", "wt-c"},
	} {
		t.Run(arm.runner, func(t *testing.T) {
			seedSiblingBlockersAs(t, ctx, store, arm.racing, arm.committed, arm.dependent)
			_, landRacingClose := beginRacingWriteOn(t, ctx, store, "close of "+arm.racing,
				[]string{arm.racing, arm.committed}, func(tx *sql.Tx) error {
					_, err := issueops.CloseIssueInTx(ctx, tx, arm.racing, "done", "tester", "")
					return err
				})

			var landed sync.Once
			var blockedInTx int
			if err := arm.run(ctx, func(tx *sql.Tx) error {
				if _, err := issueops.CloseIssueInTx(ctx, tx, arm.committed, "done", "tester", ""); err != nil {
					return err
				}
				// What this transaction's own recompute concluded, read before
				// the racing close lands so it cannot be anything else.
				if err := tx.QueryRowContext(ctx,
					"SELECT is_blocked FROM issues WHERE id = ?", arm.dependent).Scan(&blockedInTx); err != nil {
					return err
				}
				// Land the racing close inside the window: this transaction
				// began before it, so its snapshot cannot include it, and the
				// dependent is stale the moment this transaction commits.
				// sync.Once because withRetryTx may re-run this body.
				landed.Do(landRacingClose)
				return nil
			}); err != nil {
				t.Fatalf("%s: a committed close returned %v, want nil", arm.runner, err)
			}
			if blockedInTx == 0 {
				t.Fatalf("%s: %s was already unblocked inside the write's own transaction, so this arm measures nothing about the tail — the runner retried and re-ran the body on a snapshot that already included the racing close", arm.runner, arm.dependent)
			}

			// Nothing here called recheckBlockedAfterCommit or touched the
			// pending set: the runner's tail is the only thing that can have
			// settled the dependent and published the correction.
			assertIsBlocked(t, ctx, store, "issues", arm.dependent, false)
			requireCleanTables(ctx, t, store, "issues")
			if !doltHasCommitMessage(ctx, t, store, "bd: recheck blocked after close of "+arm.committed) {
				t.Fatalf("%s: the recheck corrected %s but minted no Dolt commit for it", arm.runner, arm.dependent)
			}
		})
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
