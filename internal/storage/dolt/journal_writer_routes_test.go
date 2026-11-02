package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournalWriterLock_EveryRouteAdmitsBeforeBegin pins the journal-writer
// admission on every journaled write route of the server store
// (gastownhall/beads#6117): the withRetryTx funnel, RunInTransaction, and the
// raw wisp, slot, dependency and comment transactions. A foreign session holds
// the lock; each route must be seen waiting in GET_LOCK and not return until
// the holder lets go, then succeed, and leave the lock free when it returns.
// The wisp dependency removal settles a blocked recheck after its commit, a
// journaled write of its own, so it also proves the lock is given up before
// the recheck: a route that kept it would wait on itself until busy.
func TestJournalWriterLock_EveryRouteAdmitsBeforeBegin(t *testing.T) {
	store, ctx, observer := newJournalWriterTestStore(t)
	lockName := issueops.JournalWriterLockName(store.database)
	// The observer pool has one connection, which the watch needs, so the
	// holder session comes from a pool of its own.
	holders, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	t.Cleanup(func() { _ = holders.Close() })

	issue := createJournalWriterTestIssue(t, ctx, store, "route issue")
	blocker := createJournalWriterTestIssue(t, ctx, store, "route blocker")
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: issue.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "seed"); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}
	newWisp := func(title string) *types.Issue {
		t.Helper()
		wisp := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
		if err := store.CreateIssue(ctx, wisp, "seed"); err != nil {
			t.Fatalf("create wisp %s: %v", title, err)
		}
		return wisp
	}
	wisp := newWisp("route wisp")
	wispBlocker := newWisp("route wisp blocker")
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: wisp.ID, DependsOnID: wispBlocker.ID, Type: types.DepBlocks}, "seed"); err != nil {
		t.Fatalf("seed wisp dependency: %v", err)
	}
	wispDependent := newWisp("route wisp dependent")
	slotWisp := newWisp("route slot wisp")
	if err := store.MergeMetadata(ctx, slotWisp.ID, "cleared", json.RawMessage(`"x"`), "seed"); err != nil {
		t.Fatalf("seed slot: %v", err)
	}
	closeWisp := newWisp("route close wisp")
	deleteWisp := newWisp("route delete wisp")

	routes := []struct {
		name string
		run  func(context.Context) error
	}{
		{"withRetryTx funnel", func(ctx context.Context) error {
			return store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "funnel"}, "route")
		}},
		{"RunInTransaction", func(ctx context.Context) error {
			return store.RunInTransaction(ctx, "bd: route", func(tx storage.Transaction) error {
				return tx.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "transaction"}, "route")
			})
		}},
		{"AddComment", func(ctx context.Context) error {
			return store.AddComment(ctx, issue.ID, "route", "a comment")
		}},
		{"RemoveDependency", func(ctx context.Context) error {
			return store.RemoveDependency(ctx, issue.ID, blocker.ID, "route")
		}},
		{"RemoveDependency wisp", func(ctx context.Context) error {
			return store.RemoveDependency(ctx, wisp.ID, wispBlocker.ID, "route")
		}},
		{"AddDependency wisp", func(ctx context.Context) error {
			return store.AddDependency(ctx, &types.Dependency{IssueID: wispDependent.ID, DependsOnID: wisp.ID, Type: types.DepRelated}, "route")
		}},
		{"UpdateIssue wisp", func(ctx context.Context) error {
			return store.UpdateIssue(ctx, wisp.ID, map[string]interface{}{"title": "wisp update"}, "route")
		}},
		{"ClaimIssue wisp", func(ctx context.Context) error {
			return store.ClaimIssue(ctx, wisp.ID, "route")
		}},
		{"MergeMetadata wisp", func(ctx context.Context) error {
			return store.MergeMetadata(ctx, slotWisp.ID, "merged", json.RawMessage(`"y"`), "route")
		}},
		{"SlotClear wisp", func(ctx context.Context) error {
			return store.SlotClear(ctx, slotWisp.ID, "cleared", "route")
		}},
		{"CloseIssue wisp", func(ctx context.Context) error {
			return store.CloseIssue(ctx, closeWisp.ID, "done", "route", "")
		}},
		{"DeleteIssue wisp", func(ctx context.Context) error {
			return store.DeleteIssue(ctx, deleteWisp.ID)
		}},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			holder := holdJournalWriterLock(t, ctx, holders, lockName)
			done := make(chan error, 1)
			go func() { done <- route.run(ctx) }()
			waitForJournalWriterWaiter(t, ctx, observer, store.database, nil, done, route.name)
			holder.release(t)
			if err := receiveWithin(t, done, route.name); err != nil {
				t.Fatalf("%s: %v", route.name, err)
			}
			assertJournalWriterLockFree(t, ctx, observer, lockName)
		})
	}
}

// journalWriterHolder is a foreign session holding the journal-writer lock.
type journalWriterHolder struct {
	conn *sql.Conn
	name string
}

func holdJournalWriterLock(t *testing.T, ctx context.Context, pool *sql.DB, name string) *journalWriterHolder {
	t.Helper()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("pin holder session: %v", err)
	}
	// Closing a pooled conn keeps its session, and the lock with it: let go
	// first, so a failed subtest does not hold the next one up.
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", name)
		_ = conn.Close()
	})
	var granted sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&granted); err != nil || granted.Int64 != 1 {
		t.Fatalf("holder GET_LOCK(%q) = %v, %v; want 1", name, granted, err)
	}
	return &journalWriterHolder{conn: conn, name: name}
}

func (h *journalWriterHolder) release(t *testing.T) {
	t.Helper()
	var released sql.NullInt64
	if err := h.conn.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(?)", h.name).Scan(&released); err != nil || released.Int64 != 1 {
		t.Fatalf("holder RELEASE_LOCK(%q) = %v, %v; want 1", h.name, released, err)
	}
}

// assertJournalWriterLockFree fails when any session still holds name.
func assertJournalWriterLockFree(t *testing.T, ctx context.Context, pool *sql.DB, name string) {
	t.Helper()
	var holder sql.NullInt64
	if err := pool.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&holder); err != nil {
		t.Fatalf("IS_USED_LOCK(%q): %v", name, err)
	}
	if holder.Valid {
		t.Fatalf("journal-writer lock %q is still held by session %d after the write returned", name, holder.Int64)
	}
}

// TestJournalWriterLock_NoJournalTxRefusesEmits pins the guard on the store's
// declared journal-free write route (gastownhall/beads#6117): it takes no
// journal-writer lock, so a journal emit inside it is refused before it
// allocates a seq, and the transaction cannot commit even though the body
// drops the emit's error. It is refused once, not replayed.
func TestJournalWriterLock_NoJournalTxRefusesEmits(t *testing.T) {
	store, ctx, observer := newJournalWriterTestStore(t)
	issue := createJournalWriterTestIssue(t, ctx, store, "no-journal issue")
	before := journalSeqCounter(t, ctx, observer)

	var emitErr error
	attempts := 0
	err := store.withRetryTxNoJournal(ctx, func(tx *sql.Tx) error {
		attempts++
		_, emitErr = issueops.AddIssueCommentInTx(ctx, tx, issue.ID, "route", "dropped emit")
		return nil
	})
	if !errors.Is(emitErr, issueops.ErrJournalUnlockedTx) {
		t.Fatalf("emit in a NoJournal transaction = %v, want ErrJournalUnlockedTx", emitErr)
	}
	if !errors.Is(err, issueops.ErrJournalUnlockedTx) {
		t.Fatalf("NoJournal transaction whose emit was refused = %v, want ErrJournalUnlockedTx", err)
	}
	if attempts != 1 {
		t.Fatalf("NoJournal body ran %d times, want 1: a refused emit must not be replayed", attempts)
	}
	if after := journalSeqCounter(t, ctx, observer); after != before {
		t.Fatalf("seq counter and journal rows moved from %v to %v: the refused emit allocated a seq", before, after)
	}
	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("read comments: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("the NoJournal transaction committed %d comment(s) whose emit was refused", len(comments))
	}
}

// journalSeqCounter reads the journal's seq counter and row count, which
// together move whenever a seq is allocated and committed.
func journalSeqCounter(t *testing.T, ctx context.Context, pool *sql.DB) [2]int64 {
	t.Helper()
	var got [2]int64
	if err := pool.QueryRowContext(ctx, "SELECT next_seq FROM bd_events_seq WHERE id = 0").Scan(&got[0]); err != nil {
		t.Fatalf("read seq counter: %v", err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&got[1]); err != nil {
		t.Fatalf("count journal rows: %v", err)
	}
	return got
}

// TestJournalWriterLock_HolderPastTheBudgetIsBusy pins the end of admission
// (gastownhall/beads#6117): when another session holds the journal-writer lock
// past a writer's admission deadline, the writer's body never runs and the
// write fails with the named busy error, and the lock stays the holder's.
func TestJournalWriterLock_HolderPastTheBudgetIsBusy(t *testing.T) {
	store, ctx, observer := newJournalWriterTestStore(t)
	lockName := issueops.JournalWriterLockName(store.database)
	holders, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	t.Cleanup(func() { _ = holders.Close() })
	holder := holdJournalWriterLock(t, ctx, holders, lockName)

	ran := false
	start := time.Now()
	_, err = store.commitWriteTxWith(ctx, store.db, journalTxWriter, time.Now().Add(300*time.Millisecond), func(*sql.Tx) error {
		ran = true
		return nil
	})
	if ran {
		t.Fatal("the body ran although the writer was never admitted")
	}
	var busy *issueops.JournalWriterBusyError
	if !errors.Is(err, issueops.ErrJournalWriterBusy) || !errors.As(err, &busy) {
		t.Fatalf("write past the admission deadline = %v, want a *JournalWriterBusyError", err)
	}
	if busy.Lock != lockName || busy.Database != store.database {
		t.Fatalf("busy error names lock %q for database %q, want %q for %q", busy.Lock, busy.Database, lockName, store.database)
	}
	if waited := time.Since(start); waited > 10*time.Second {
		t.Fatalf("admission gave up after %s, want it bounded by its 300ms deadline", waited)
	}
	var user sql.NullInt64
	if err := observer.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", lockName).Scan(&user); err != nil {
		t.Fatalf("IS_USED_LOCK: %v", err)
	}
	var holderID int64
	if err := holder.conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&holderID); err != nil {
		t.Fatalf("holder CONNECTION_ID: %v", err)
	}
	if !user.Valid || user.Int64 != holderID {
		t.Fatalf("journal-writer lock held by %v after a busy refusal, want the holder session %d", user, holderID)
	}
}

// TestJournalWriterLock_AdvisoryDeferWakeSkipsWhenContended pins the ready
// path's advisory defer-wake sweep under a contended journal
// (gastownhall/beads#6117): it makes one non-waiting attempt at the
// journal-writer lock and, finding it held, skips at once, with no warning and
// no queued wait. The next sweep, uncontended, wakes the defer.
func TestJournalWriterLock_AdvisoryDeferWakeSkipsWhenContended(t *testing.T) {
	store, ctx, observer := newJournalWriterTestStore(t)
	lockName := issueops.JournalWriterLockName(store.database)
	holders, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	t.Cleanup(func() { _ = holders.Close() })

	issue := createJournalWriterTestIssue(t, ctx, store, "expired defer")
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"status":      "deferred",
		"defer_until": time.Now().UTC().Add(-time.Hour),
	}, "seed"); err != nil {
		t.Fatalf("defer: %v", err)
	}
	status := func() types.Status {
		t.Helper()
		got, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("get %s: %v", issue.ID, err)
		}
		return got.Status
	}

	holder := holdJournalWriterLock(t, ctx, holders, lockName)
	start := time.Now()
	warned := captureOpenStderr(t, func() { store.WakeExpiredDefersAdvisory(ctx) })
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the contended sweep returned after %s; want one non-waiting attempt", waited)
	}
	if warned != "" {
		t.Fatalf("the contended sweep warned %q; want a silent skip", warned)
	}
	if got := status(); got != types.StatusDeferred {
		t.Fatalf("status after the contended sweep = %q, want it still %q", got, types.StatusDeferred)
	}

	holder.release(t)
	if warned := captureOpenStderr(t, func() { store.WakeExpiredDefersAdvisory(ctx) }); warned != "" {
		t.Fatalf("the uncontended sweep warned %q", warned)
	}
	if got := status(); got != types.StatusOpen {
		t.Fatalf("status after the uncontended sweep = %q, want %q", got, types.StatusOpen)
	}
	assertJournalWriterLockFree(t, ctx, observer, lockName)
}
