package dolt

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// overlapCreates runs two transactions that each create one issue and holds
// both open until both have written, then commits the winner before the
// loser. With the events journal on, every mutation also bumps
// the single bd_events_seq row inside its own transaction, so two writers
// that overlap at all collide at commit time. run decides how the loser's
// transaction is driven; the barrier holds only on each side's first attempt.
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

func countRowsByID(t *testing.T, ctx context.Context, db *sql.DB, table, id string) int {
	t.Helper()
	var n int
	//nolint:gosec // G202: table is a fixed literal chosen by the test
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	return n
}

// TestRunInTransactionLostJournalConflictIsMarked reproduces the field
// failure exactly: `bd mol wisp` from two processes at once, events journal on,
// the second commit refused with "sql commit (regular): Error 1213 ...". The
// raw RunInTransaction keeps its at-most-once contract — it does not replay —
// but it now says the failure is a rolled-back conflict a caller may replay.
func TestRunInTransactionLostJournalConflictIsMarked(t *testing.T) {
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

// TestRunReplayableTransactionReplaysLostJournalConflict is the fix: the same
// overlap, driven through storage.RunReplayableTransaction, replays the
// loser's WHOLE transaction. Both writes land exactly once and the journal
// records exactly one create for each — nothing half-written, nothing doubled.
func TestRunReplayableTransactionReplaysLostJournalConflict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ephemeral bool
		table     string
	}{
		{name: "wisp (bd mol wisp)", ephemeral: true, table: "wisps"},
		{name: "durable (bd mol pour, bd batch)", ephemeral: false, table: "issues"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := concurrentTestContext(t)
			defer cancel()
			peer := openPeerStore(t, ctx, store)
			store.SetEventsJournalEnabled(true)
			peer.SetEventsJournalEnabled(true)

			var journalBefore int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&journalBefore); err != nil {
				t.Fatalf("count journal rows: %v", err)
			}

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
				if n := countRowsByID(t, ctx, store.db, tc.table, id); n != 1 {
					t.Fatalf("%s rows for %s = %d, want exactly 1", tc.table, id, n)
				}
			}
			var journalAfter int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&journalAfter); err != nil {
				t.Fatalf("count journal rows: %v", err)
			}
			var perCreate int
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ?", ids[0]).Scan(&perCreate); err != nil {
				t.Fatalf("count winner journal rows: %v", err)
			}
			if journalAfter-journalBefore != 2*perCreate {
				t.Fatalf("journal grew by %d rows, want %d (the same per-create journal footprint for winner and loser; a replay must not double-record)",
					journalAfter-journalBefore, 2*perCreate)
			}
		})
	}
}
