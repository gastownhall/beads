package dolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// journalWriterWatchdog bounds every wait in these tests, so a regression
// fails them instead of hanging the package.
const journalWriterWatchdog = 20 * time.Second

// newJournalWriterTestStore is an isolated server-mode store with the journal
// on, and an observer pool for watching its sessions from outside.
func newJournalWriterTestStore(t *testing.T) (*DoltStore, context.Context, *sql.DB) {
	t.Helper()
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	store.SetEventsJournalEnabled(true)
	observer, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open observer: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	return store, ctx, observer
}

func createJournalWriterTestIssue(t *testing.T, ctx context.Context, store *DoltStore, title string) *types.Issue {
	t.Helper()
	issue := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("create %s: %v", title, err)
	}
	return issue
}

// journalHead is the highest journal seq so far.
func journalHead(t *testing.T, ctx context.Context, store *DoltStore) int64 {
	t.Helper()
	rows, err := store.ReadEventsJournal(ctx, 0, 10000)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var head int64
	for _, row := range rows {
		if row.Seq > head {
			head = row.Seq
		}
	}
	return head
}

// waitForJournalWriterWaiter returns once a session of database is observed
// in a GET_LOCK on the journal-writer lock. It fails the test if began fires
// first: that writer opened its transaction without waiting for admission.
func waitForJournalWriterWaiter(t *testing.T, ctx context.Context, observer *sql.DB, database string, began <-chan struct{}, done <-chan error, who string) {
	t.Helper()
	const snippet = "get_lock('bd_events_seq:"
	deadline := time.Now().Add(journalWriterWatchdog)
	for time.Now().Before(deadline) {
		select {
		case <-began:
			t.Fatalf("%s began its transaction while another journal writer was inside its own; want it held at admission", who)
		case err := <-done:
			t.Fatalf("%s returned (%v) while another journal writer was inside its transaction; want it held at admission", who, err)
		default:
		}
		waiting, err := sessionRunning(ctx, observer, database, snippet)
		if err != nil {
			t.Fatalf("observe %s: %v", who, err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never observed waiting for the journal-writer lock", who)
}

// sessionRunning reports whether a session on database is running a statement
// containing snippet (case-insensitive), per SHOW PROCESSLIST.
func sessionRunning(ctx context.Context, observer *sql.DB, database, snippet string) (bool, error) {
	rows, err := observer.QueryContext(ctx, "SHOW PROCESSLIST")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	infoIndex, databaseIndex := -1, -1
	for i, column := range columns {
		switch {
		case strings.EqualFold(column, "info"):
			infoIndex = i
		case strings.EqualFold(column, "db"):
			databaseIndex = i
		}
	}
	if infoIndex < 0 || databaseIndex < 0 {
		return false, errors.New("SHOW PROCESSLIST has no db or info column")
	}
	values := make([]sql.NullString, len(columns))
	scanArgs := make([]any, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	snippet = strings.ToLower(snippet)
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return false, err
		}
		if values[databaseIndex].String == database && strings.Contains(strings.ToLower(values[infoIndex].String), snippet) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func receiveWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(journalWriterWatchdog):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// TestJournalWriterLock_ConcurrentWritersSerialize is the gastownhall/beads#6117
// regression on its deterministic schedule: writer A is inside its journaled
// transaction, paused after its emit; writer B must wait at admission rather
// than begin, and once A is released both commit exactly once, journaled at
// contiguous seqs in commit order. On the unfixed store B begins and commits
// inside A's window and A's commit then loses the counter conflict (1213) and
// replays its body.
func TestJournalWriterLock_ConcurrentWritersSerialize(t *testing.T) {
	store, ctx, observer := newJournalWriterTestStore(t)
	issueA := createJournalWriterTestIssue(t, ctx, store, "writer A target")
	issueB := createJournalWriterTestIssue(t, ctx, store, "writer B target")
	head := journalHead(t, ctx, store)

	var attemptsA, attemptsB atomic.Int32
	aInside := make(chan struct{})
	releaseA := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseA) }) }
	t.Cleanup(release)

	aDone := make(chan error, 1)
	go func() {
		aDone <- store.withRetryTx(ctx, func(tx *sql.Tx) error {
			attempt := attemptsA.Add(1)
			if _, err := issueops.UpdateIssueInTx(ctx, tx, issueA.ID, map[string]interface{}{"title": "written by A"}, "writer-a"); err != nil {
				return err
			}
			if attempt == 1 {
				close(aInside)
				<-releaseA
			}
			return nil
		})
	}()
	receiveWithin(t, aInside, "writer A to emit inside its transaction")

	bBegan := make(chan struct{}, 1)
	bDone := make(chan error, 1)
	go func() {
		bDone <- store.withRetryTx(ctx, func(tx *sql.Tx) error {
			attemptsB.Add(1)
			select {
			case bBegan <- struct{}{}:
			default:
			}
			_, err := issueops.UpdateIssueInTx(ctx, tx, issueB.ID, map[string]interface{}{"title": "written by B"}, "writer-b")
			return err
		})
	}()
	waitForJournalWriterWaiter(t, ctx, observer, store.database, bBegan, bDone, "writer B")

	release()
	if err := receiveWithin(t, aDone, "writer A"); err != nil {
		t.Fatalf("writer A: %v", err)
	}
	if err := receiveWithin(t, bDone, "writer B"); err != nil {
		t.Fatalf("writer B: %v", err)
	}
	if got := attemptsA.Load(); got != 1 {
		t.Errorf("writer A ran its body %d times, want 1 (a replay means the counter conflict was not prevented)", got)
	}
	if got := attemptsB.Load(); got != 1 {
		t.Errorf("writer B ran its body %d times, want 1", got)
	}
	for id, want := range map[string]string{issueA.ID: "written by A", issueB.ID: "written by B"} {
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.Title != want {
			t.Errorf("%s title = %q, want %q", id, got.Title, want)
		}
	}
	assertJournalCommitOrder(t, ctx, store, head, issueA.ID, issueB.ID)
}

// assertJournalCommitOrder checks the journal rows after head: contiguous
// seqs, at least one row per writer, and every row of the first writer before
// any of the second's.
func assertJournalCommitOrder(t *testing.T, ctx context.Context, store *DoltStore, head int64, first, second string) {
	t.Helper()
	rows, err := store.ReadEventsJournal(ctx, head, 10000)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var sawFirst, sawSecond bool
	for i, row := range rows {
		if want := head + int64(i) + 1; row.Seq != want {
			t.Fatalf("journal row %d has seq %d, want %d (gapless)", i, row.Seq, want)
		}
		switch row.IssueID {
		case first:
			if sawSecond {
				t.Fatalf("seq %d journals %s after %s's rows; want commit order", row.Seq, first, second)
			}
			sawFirst = true
		case second:
			sawSecond = true
		}
	}
	if !sawFirst || !sawSecond {
		t.Fatalf("journal after seq %d has rows for %s=%v %s=%v, want both", head, first, sawFirst, second, sawSecond)
	}
}

// TestJournalWriterLock_TransactionHolderQueuesAFunnelWriter is the
// gastownhall/beads#6117 schedule across the store's two writer shapes: the
// holder is a RunInTransaction (admitted before its snapshot reads and BEGIN)
// paused after its emit, and the waiter is a plain withRetryTx funnel write.
// The waiter must be seen waiting at admission, not inside a transaction, and
// once the holder commits both bodies have run exactly once and the journal
// is contiguous in commit order.
func TestJournalWriterLock_TransactionHolderQueuesAFunnelWriter(t *testing.T) {
	store, ctx, observer := newJournalWriterTestStore(t)
	target := createJournalWriterTestIssue(t, ctx, store, "funnel waiter target")
	head := journalHead(t, ctx, store)

	holderIssue := &types.Issue{Title: "transaction holder issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	var holderRuns, waiterRuns atomic.Int32
	inside := make(chan struct{})
	releaseHolder := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHolder) }) }
	t.Cleanup(release)

	holderDone := make(chan error, 1)
	go func() {
		holderDone <- store.RunInTransaction(ctx, "transaction holder", func(tx storage.Transaction) error {
			run := holderRuns.Add(1)
			if err := tx.CreateIssue(ctx, holderIssue, "holder"); err != nil {
				return err
			}
			if run == 1 {
				close(inside)
				<-releaseHolder
			}
			return nil
		})
	}()
	receiveWithin(t, inside, "the transaction holder to emit inside its transaction")

	waiterBegan := make(chan struct{}, 1)
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- store.withRetryTx(ctx, func(tx *sql.Tx) error {
			waiterRuns.Add(1)
			select {
			case waiterBegan <- struct{}{}:
			default:
			}
			_, err := issueops.UpdateIssueInTx(ctx, tx, target.ID, map[string]interface{}{"title": "written by the funnel waiter"}, "waiter")
			return err
		})
	}()
	waitForJournalWriterWaiter(t, ctx, observer, store.database, waiterBegan, waiterDone, "the funnel waiter")

	release()
	if err := receiveWithin(t, holderDone, "the transaction holder"); err != nil {
		t.Fatalf("transaction holder: %v", err)
	}
	if err := receiveWithin(t, waiterDone, "the funnel waiter"); err != nil {
		t.Fatalf("funnel waiter: %v", err)
	}
	if got := holderRuns.Load(); got != 1 {
		t.Errorf("transaction holder ran its body %d times, want 1 (a replay means the counter conflict was not prevented)", got)
	}
	if got := waiterRuns.Load(); got != 1 {
		t.Errorf("funnel waiter ran its body %d times, want 1", got)
	}
	assertJournalCommitOrder(t, ctx, store, head, holderIssue.ID, target.ID)
}

// TestJournalWriterLock_OtherDatabasesWriterDoesNotQueue pins that the lock is
// per database (gastownhall/beads#6117): a session holding another database's
// journal-writer lock, as that database's writer does inside its transaction,
// does not hold this store's writers. The write completes, journaled, and the
// other database's lock is still its holder's.
func TestJournalWriterLock_OtherDatabasesWriterDoesNotQueue(t *testing.T) {
	store, ctx, observer := newJournalWriterTestStore(t)
	other := store.database + "_other"
	otherName := issueops.JournalWriterLockName(other)
	if otherName == issueops.JournalWriterLockName(store.database) {
		t.Fatalf("databases %q and %q share the journal-writer lock %q", store.database, other, otherName)
	}

	holder, err := observer.Conn(ctx)
	if err != nil {
		t.Fatalf("pin holder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = holder.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", otherName)
		_ = holder.Close()
	})
	var granted sql.NullInt64
	if err := holder.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", otherName).Scan(&granted); err != nil || granted.Int64 != 1 {
		t.Fatalf("hold %q: granted=%v err=%v", otherName, granted, err)
	}

	head := journalHead(t, ctx, store)
	start := time.Now()
	issue := createJournalWriterTestIssue(t, ctx, store, "written beside another database's writer")
	if waited := time.Since(start); waited >= issueops.JournalWriterAdmissionBudget/2 {
		t.Fatalf("the write took %v beside another database's held lock; want no queueing", waited)
	}
	if got := journalHead(t, ctx, store); got <= head {
		t.Fatalf("journal head %d after the write, want past %d (the write %s was not journaled)", got, head, issue.ID)
	}
	var stillHeld sql.NullBool
	if err := holder.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?) = CONNECTION_ID()", otherName).Scan(&stillHeld); err != nil {
		t.Fatalf("IS_USED_LOCK: %v", err)
	}
	if !stillHeld.Bool {
		t.Fatalf("the other database's lock %q is no longer its holder's after this store's write", otherName)
	}
}
