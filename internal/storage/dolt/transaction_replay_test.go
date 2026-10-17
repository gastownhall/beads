package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// With the events journal on, every mutation also bumps the single
// bd_events_seq row inside its own transaction, so two writers whose
// transactions overlap at all collide when the second one commits: Dolt rolls
// it back with MySQL 1213 ("serialization failure ... try restarting
// transaction"). These tests order two such transactions deterministically:
// both write, the winner commits, then the loser commits and loses.

// overlapCreates runs two transactions that each create one issue, holds both
// open until both have written, then lets the winner commit before the loser.
// run decides how each transaction is driven; the hold applies only to each
// transaction's first attempt.
func overlapCreates(t *testing.T, ctx context.Context, winner, loser *DoltStore, ephemeral bool,
	run func(s *DoltStore, fn func(tx storage.Transaction) error) error,
) (winnerErr, loserErr error, loserCallbacks int32, ids [2]string) {
	t.Helper()
	prepared := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	done := [2]chan error{make(chan error, 1), make(chan error, 1)}
	var calls [2]atomic.Int32
	for i, s := range []*DoltStore{winner, loser} {
		go func() {
			done[i] <- run(s, func(tx storage.Transaction) error {
				attempt := calls[i].Add(1)
				issue := &types.Issue{
					Title:     []string{"winner", "loser"}[i],
					Status:    types.StatusOpen,
					Priority:  2,
					IssueType: types.TypeTask,
					Ephemeral: ephemeral,
				}
				if err := tx.CreateIssue(ctx, issue, "tester"); err != nil {
					return err
				}
				ids[i] = issue.ID
				if attempt == 1 {
					close(prepared[i])
					return waitForTransactionRelease(ctx, release[i])
				}
				return nil
			})
		}()
	}
	waitForTransactionPrepared(t, ctx, prepared[0], "winner")
	waitForTransactionPrepared(t, ctx, prepared[1], "loser")
	close(release[0])
	winnerErr = <-done[0]
	close(release[1])
	loserErr = <-done[1]
	return winnerErr, loserErr, calls[1].Load(), ids
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, table, column, value string) int {
	t.Helper()
	var n int
	//nolint:gosec // G202: table and column are fixed literals chosen by the test
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+" = ?", value).Scan(&n); err != nil {
		t.Fatalf("count %s rows with %s = %s: %v", table, column, value, err)
	}
	return n
}

func journalRows(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&n); err != nil {
		t.Fatalf("count journal rows: %v", err)
	}
	return n
}

// TestRunInTransactionMarksLostJournalConflict: RunInTransaction keeps its
// at-most-once contract and does not replay the loser, but marks its failure
// as a rolled-back conflict so a caller whose callback is safe to repeat can.
func TestRunInTransactionMarksLostJournalConflict(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	peer := openPeerStore(t, ctx, store)
	store.SetEventsJournalEnabled(true)
	peer.SetEventsJournalEnabled(true)

	winnerErr, loserErr, loserCalls, _ := overlapCreates(t, ctx, store, peer, true,
		func(s *DoltStore, fn func(tx storage.Transaction) error) error {
			return s.RunInTransaction(ctx, "bd: clone template subgraph", fn)
		})
	if winnerErr != nil {
		t.Fatalf("winner: %v", winnerErr)
	}
	if loserErr == nil {
		t.Fatal("loser committed although both transactions bumped the journal counter; the overlap did not conflict")
	}
	if !errors.Is(loserErr, storage.ErrTransactionConflict) {
		t.Fatalf("loser error = %v, want it marked storage.ErrTransactionConflict", loserErr)
	}
	if errors.Is(loserErr, ErrCommitIndeterminate) {
		t.Fatalf("loser error = %v must not be indeterminate: the server rolled it back", loserErr)
	}
	if loserCalls != 1 {
		t.Fatalf("loser callback ran %d times, want 1 (RunInTransaction never replays)", loserCalls)
	}
}

// TestRunReplayableTransactionReplaysLostJournalConflict drives the same
// overlap through storage.RunReplayableTransaction: the loser's WHOLE
// transaction is replayed once, both creates land exactly once, and the
// journal records exactly one create for each.
func TestRunReplayableTransactionReplaysLostJournalConflict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ephemeral bool
		table     string
	}{
		{name: "wisp", ephemeral: true, table: "wisps"},
		{name: "durable", ephemeral: false, table: "issues"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := concurrentTestContext(t)
			defer cancel()
			peer := openPeerStore(t, ctx, store)
			store.SetEventsJournalEnabled(true)
			peer.SetEventsJournalEnabled(true)
			journalBefore := journalRows(t, ctx, store.db)

			winnerErr, loserErr, loserCalls, ids := overlapCreates(t, ctx, store, peer, tc.ephemeral,
				func(s *DoltStore, fn func(tx storage.Transaction) error) error {
					return storage.RunReplayableTransaction(ctx, s, "bd: clone template subgraph", fn)
				})
			if winnerErr != nil {
				t.Fatalf("winner: %v", winnerErr)
			}
			if loserErr != nil {
				t.Fatalf("loser was not replayed to success: %v", loserErr)
			}
			if loserCalls != 2 {
				t.Fatalf("loser callback ran %d times, want 2 (one lost attempt, one replay)", loserCalls)
			}
			for i, id := range ids {
				if id == "" {
					t.Fatalf("transaction %d reported no issue id", i)
				}
				if n := countRows(t, ctx, store.db, tc.table, "id", id); n != 1 {
					t.Fatalf("%s rows for %s = %d, want exactly 1", tc.table, id, n)
				}
			}
			perCreate := countRows(t, ctx, store.db, "bd_events_journal", "issue_id", ids[0])
			if grew := journalRows(t, ctx, store.db) - journalBefore; grew != 2*perCreate {
				t.Fatalf("journal grew by %d rows, want %d: the replay must record its create exactly as the winner did", grew, 2*perCreate)
			}
		})
	}
}

// TestRunReplayableTransactionNeverReplaysLostCommitResponse: a COMMIT whose
// response is lost may have landed, so it surfaces as indeterminate and is
// never replayed, with the journal on (one transaction) or off (a regular and
// an ignored transaction).
func TestRunReplayableTransactionNeverReplaysLostCommitResponse(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	proxy := startSQLWireProxy(t)
	via := openStoreOnPort(t, ctx, base, proxy.port())

	for _, journal := range []bool{false, true} {
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			via.SetEventsJournalEnabled(journal)
			title := fmt.Sprintf("lost-commit-journal-%v", journal)
			var calls atomic.Int32
			sprung := proxy.dropResponseToNext(isCommitStatement)
			err := storage.RunReplayableTransaction(ctx, via, "bd: lost commit", func(tx storage.Transaction) error {
				calls.Add(1)
				return tx.CreateIssue(ctx, &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester")
			})
			select {
			case <-sprung:
			default:
				t.Fatalf("the transaction never reached COMMIT (err = %v)", err)
			}
			if err == nil {
				t.Fatal("a lost commit response reported success")
			}
			if !errors.Is(err, ErrCommitIndeterminate) || errors.Is(err, storage.ErrTransactionConflict) {
				t.Fatalf("err = %v, want indeterminate and not a replayable conflict", err)
			}
			if n := calls.Load(); n != 1 {
				t.Fatalf("callback ran %d times: a lost commit response must never be replayed", n)
			}
			if n := countRows(t, ctx, base.db, "issues", "title", title); n != 1 {
				t.Fatalf("rows titled %q = %d, want 1: the server committed it once", title, n)
			}
		})
	}
}

// splitTxOverlap runs a winner on peer and a loser on store, both with the
// journal off, so each runs the default split shape: a regular transaction
// for Dolt-tracked tables and an ignored one for the wisp tables. Both write,
// the winner commits, then the loser commits.
func splitTxOverlap(t *testing.T, ctx context.Context, store, peer *DoltStore,
	winnerWrite, loserWrite func(tx storage.Transaction) error,
) (loserErr error, loserCalls int32) {
	t.Helper()
	prepared := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	done := [2]chan error{make(chan error, 1), make(chan error, 1)}
	var calls atomic.Int32
	go func() {
		done[0] <- peer.RunInTransaction(ctx, "winner", func(tx storage.Transaction) error {
			if err := winnerWrite(tx); err != nil {
				return err
			}
			close(prepared[0])
			return waitForTransactionRelease(ctx, release[0])
		})
	}()
	go func() {
		done[1] <- storage.RunReplayableTransaction(ctx, store, "loser", func(tx storage.Transaction) error {
			attempt := calls.Add(1)
			if err := loserWrite(tx); err != nil {
				return err
			}
			if attempt == 1 {
				close(prepared[1])
				return waitForTransactionRelease(ctx, release[1])
			}
			return nil
		})
	}()
	waitForTransactionPrepared(t, ctx, prepared[0], "winner")
	waitForTransactionPrepared(t, ctx, prepared[1], "loser")
	close(release[0])
	if err := <-done[0]; err != nil {
		t.Fatalf("winner: %v", err)
	}
	close(release[1])
	return <-done[1], calls.Load()
}

// TestSplitTransactionRegularConflictRollsBackBothPlanes: the loser's REGULAR
// commit loses, so both of its transactions roll back and the replay writes
// each plane exactly once; no wisp from the lost attempt survives.
func TestSplitTransactionRegularConflictRollsBackBothPlanes(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	peer := openPeerStore(t, ctx, store)
	createPerm(t, ctx, store, "test-shared")

	loserErr, loserCalls := splitTxOverlap(t, ctx, store, peer,
		func(tx storage.Transaction) error {
			return tx.UpdateIssue(ctx, "test-shared", map[string]interface{}{"title": "winner"}, "w")
		},
		func(tx storage.Transaction) error {
			if err := tx.UpdateIssue(ctx, "test-shared", map[string]interface{}{"title": "loser"}, "l"); err != nil {
				return err
			}
			return tx.CreateIssue(ctx, &types.Issue{Title: "split-wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "l")
		})
	if loserErr != nil {
		t.Fatalf("loser: %v", loserErr)
	}
	if loserCalls != 2 {
		t.Fatalf("loser attempts = %d, want 2", loserCalls)
	}
	if n := countRows(t, ctx, store.db, "wisps", "title", "split-wisp"); n != 1 {
		t.Fatalf("wisp rows = %d, want 1: the lost attempt's ignored-plane write leaked", n)
	}
	got, err := store.GetIssue(ctx, "test-shared")
	if err != nil {
		t.Fatalf("read test-shared: %v", err)
	}
	if got.Title != "loser" {
		t.Fatalf("title = %q, want the replayed loser's", got.Title)
	}
}

// TestSplitTransactionIgnoredConflictAfterRegularCommitIsNotReplayed: the
// loser's IGNORED commit loses after its regular commit landed. That outcome
// is partial, so it is indeterminate and never a replayable conflict.
func TestSplitTransactionIgnoredConflictAfterRegularCommitIsNotReplayed(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	peer := openPeerStore(t, ctx, store)
	createWisp(t, ctx, store, "test-wisp-shared")

	loserErr, loserCalls := splitTxOverlap(t, ctx, store, peer,
		func(tx storage.Transaction) error {
			return tx.UpdateIssue(ctx, "test-wisp-shared", map[string]interface{}{"title": "winner"}, "w")
		},
		func(tx storage.Transaction) error {
			if err := tx.CreateIssue(ctx, &types.Issue{Title: "split-durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "l"); err != nil {
				return err
			}
			return tx.UpdateIssue(ctx, "test-wisp-shared", map[string]interface{}{"title": "loser"}, "l")
		})
	if loserErr == nil {
		t.Fatal("loser succeeded; the ignored-plane overlap did not conflict")
	}
	if errors.Is(loserErr, storage.ErrTransactionConflict) || !errors.Is(loserErr, ErrCommitIndeterminate) {
		t.Fatalf("err = %v, want indeterminate, not a replayable conflict", loserErr)
	}
	if loserCalls != 1 {
		t.Fatalf("loser attempts = %d, want 1: no replay after the regular commit landed", loserCalls)
	}
	if n := countRows(t, ctx, store.db, "issues", "title", "split-durable"); n != 1 {
		t.Fatalf("durable rows = %d, want 1", n)
	}
}
