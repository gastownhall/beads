package dolt

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournalWriterLock_ConcurrentWritersSerialize pins AC-1 of
// gastownhall/beads#6117: with the events journal on in server mode, a second
// journaled writer's transaction may not BEGIN while a first writer is paused
// inside its own — the waiter queues on the journal-writer admission lock —
// and once the holder commits, both writes land with contiguous journal
// sequences in SQL commit order and the waiter's body ran exactly once (no
// counter-conflict replay). On the unfixed store the waiter's body starts
// inside the holder's snapshot, so the holder's commit aborts on the single
// bd_events_seq row and the write path replays it.
func TestJournalWriterLock_ConcurrentWritersSerialize(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	defer store.SetEventsJournalEnabled(false)

	newIssue := func(title string) *types.Issue {
		return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}

	// The waiter's target row, created before the experiment so the waiter's
	// body is one journaled update.
	target := newIssue("waiter target")
	if err := store.CreateIssue(ctx, target, "actor"); err != nil {
		t.Fatalf("create target issue: %v", err)
	}

	// Writer A: a journaled writer that pauses INSIDE its transaction after
	// emitting, holding the journal-writer admission lock while paused.
	writerA := newIssue("writer A issue")
	aInside := make(chan struct{}, 1)
	releaseA := make(chan struct{})
	longDone := make(chan error, 1)
	go func() {
		longDone <- store.RunInTransaction(ctx, "writer A", func(tx storage.Transaction) error {
			if err := tx.CreateIssue(ctx, writerA, "actor"); err != nil {
				return err
			}
			aInside <- struct{}{}
			select {
			case <-releaseA:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	// Watchdog: A must reach its pause; the test never waits unbounded.
	select {
	case <-aInside:
	case <-time.After(30 * time.Second):
		close(releaseA)
		t.Fatalf("writer A never entered its transaction")
	}

	// Writer B: a journaled writer through the store funnel. Its body may run
	// at most once and signals when it starts.
	var bRuns int
	bStarted := make(chan struct{}, 1)
	shortDone := make(chan error, 1)
	go func() {
		shortDone <- store.withRetryTx(ctx, func(tx *sql.Tx) error {
			bRuns++
			select {
			case bStarted <- struct{}{}:
			default:
			}
			if _, err := issueops.UpdateIssueInTx(ctx, tx, target.ID, map[string]interface{}{"status": types.StatusInProgress}, "actor-b"); err != nil {
				return err
			}
			return nil
		})
	}()

	// The AC-1 assertion: while A is paused inside its transaction, B does
	// not BEGIN. On the unfixed store B's body starts during A's snapshot.
	select {
	case <-bStarted:
		close(releaseA)
		<-longDone
		<-shortDone
		t.Fatalf("waiter began its transaction while the holder was inside its own")
	case <-time.After(1500 * time.Millisecond):
	}

	// Release A; both writers must complete.
	close(releaseA)
	select {
	case err := <-longDone:
		if err != nil {
			t.Fatalf("holder writer failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("holder writer never completed")
	}
	select {
	case err := <-shortDone:
		if err != nil {
			t.Fatalf("waiter writer failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("waiter writer never completed")
	}
	if bRuns != 1 {
		t.Fatalf("waiter body ran %d times, want exactly 1 (no counter-conflict replay)", bRuns)
	}

	// Both writes are durable with contiguous journal sequences in SQL commit
	// order: the sequence is gapless, A's row precedes B's, and B's update is
	// the last row — it committed after the holder released the lock.
	rows, err := store.ReadEventsJournal(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("read events journal: %v", err)
	}
	if len(rows) < 3 {
		t.Fatalf("journal rows = %d, want >= 3", len(rows))
	}
	slices.SortFunc(rows, func(a, b storage.EventsJournalRow) int {
		return int(a.Seq - b.Seq)
	})
	for i, r := range rows {
		if r.Seq != rows[0].Seq+int64(i) {
			t.Fatalf("journal seq %d at index %d breaks the contiguous sequence (first seq %d)", r.Seq, i, rows[0].Seq)
		}
	}
	last := rows[len(rows)-1]
	if last.IssueID != target.ID {
		t.Fatalf("last journal row is %s, want the waiter's update of %s (SQL commit order)", last.IssueID, target.ID)
	}
	holderSeq := int64(-1)
	for _, r := range rows {
		if r.IssueID == writerA.ID {
			holderSeq = r.Seq
		}
	}
	if holderSeq < 0 {
		t.Fatalf("no journal row for the holder's issue %s", writerA.ID)
	}
	if holderSeq >= last.Seq {
		t.Fatalf("holder journal seq %d is not before the waiter's %d (SQL commit order)", holderSeq, last.Seq)
	}
}
// TestJournalWriterLock_DisabledJournalTakesNoLock pins AC-3 of
// gastownhall/beads#6117: with the journal off, a server-mode store takes no
// journal-writer admission lock — a write proceeds promptly while another
// session HOLDS the lock — and emission stays a no-op (no journal rows).
func TestJournalWriterLock_DisabledJournalTakesNoLock(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	// The journal stays OFF — the test harness default.

	// A probe session holds the journal-writer admission lock, standing in
	// for another admitted writer on this database. A journal-off write that
	// took admission would queue behind it and blow the short write deadline
	// below; a no-lock write completes promptly.
	probe, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin probe connection: %v", err)
	}
	defer func() { _ = probe.Close() }()
	lockName := issueops.JournalWriterLockName(store.database)
	var granted sql.NullInt64
	if err := probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted); err != nil {
		t.Fatalf("take probe lock: %v", err)
	}
	if !granted.Valid || granted.Int64 != 1 {
		t.Fatalf("probe could not take the journal-writer lock (granted=%v)", granted)
	}
	defer func() { _, _ = probe.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName) }()

	// A funnel write and a raw-site write, both promptly, while the lock is
	// held: neither route issues journal-lock SQL when the journal is off.
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	issue := &types.Issue{Title: "journal off", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(wctx, issue, "actor"); err != nil {
		t.Fatalf("journal-off funnel write queued or failed behind the lock: %v", err)
	}
	if err := store.AddComment(wctx, issue.ID, "actor", "comment"); err != nil {
		t.Fatalf("journal-off raw-site write queued or failed behind the lock: %v", err)
	}

	// Emission stays a no-op with the journal off.
	rows, err := store.ReadEventsJournal(ctx, 0, 100)
	if err != nil {
		t.Fatalf("read events journal: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("journal rows = %d, want 0 (journal-off emission stays a no-op)", len(rows))
	}
}

// TestJournalUnlockedScope_NoJournalFunnelCannotEmit pins AC-4 of
// gastownhall/beads#6117 for the store side: an audited NoJournal write
// (withRetryTxNoJournal) with the journal ON takes no journal-writer
// admission lock, and its transaction is scoped journal-FORBIDDEN — an emit
// attempt is refused with ErrJournalUnlockedTx before any sequence is
// allocated, and the latched violation refuses the COMMIT even when the
// callback ignores the emit error. With the journal OFF the same funnel keeps
// the no-op: an emit stays silent and the write succeeds.
func TestJournalUnlockedScope_NoJournalFunnelCannotEmit(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	defer store.SetEventsJournalEnabled(false)

	journalRows := func() int {
		rows, err := store.ReadEventsJournal(ctx, 0, 1000)
		if err != nil {
			t.Fatalf("read events journal: %v", err)
		}
		return len(rows)
	}

	// A propagated emit refusal: no row lands, no sequence is drawn.
	err := store.withRetryTxNoJournal(ctx, func(tx *sql.Tx) error {
		return issueops.RecordDeleteInTx(ctx, tx, "bd-nojournal", "actor")
	})
	if !errors.Is(err, issueops.ErrJournalUnlockedTx) {
		t.Fatalf("NoJournal emit error = %v, want ErrJournalUnlockedTx", err)
	}
	if n := journalRows(); n != 0 {
		t.Fatalf("journal rows = %d after a refused emit, want 0", n)
	}

	// A callback that ignores the emit error still cannot commit: the write it
	// made is rolled back with the refusal. UpdateIssueInTx is itself a
	// journaling mutator, so its emit is the refused one.
	target := &types.Issue{Title: "nojournal target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, target, "actor"); err != nil {
		t.Fatalf("create target issue: %v", err)
	}
	err = store.withRetryTxNoJournal(ctx, func(tx *sql.Tx) error {
		if _, err := issueops.UpdateIssueInTx(ctx, tx, target.ID, map[string]interface{}{"status": types.StatusInProgress}, "actor-b"); err != nil {
			_ = err // ignored: the commit check must refuse this transaction
		}
		return nil
	})
	if !errors.Is(err, issueops.ErrJournalUnlockedTx) {
		t.Fatalf("commit after an ignored emit error = %v, want ErrJournalUnlockedTx", err)
	}
	reread, err := store.GetIssue(ctx, target.ID)
	if err != nil {
		t.Fatalf("reread target issue: %v", err)
	}
	if reread.Status != types.StatusOpen {
		t.Fatalf("target status = %q, want it rolled back to open", reread.Status)
	}
	if n := journalRows(); n != 1 {
		t.Fatalf("journal rows = %d, want exactly the funnel create's 1 (no NoJournal row)", n)
	}

	// No admission: with the journal ON and a probe holding the journal-writer
	// lock, a NoJournal write completes promptly — it never queues.
	probe, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin probe connection: %v", err)
	}
	defer func() { _ = probe.Close() }()
	lockName := issueops.JournalWriterLockName(store.database)
	var granted sql.NullInt64
	if err := probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted); err != nil {
		t.Fatalf("take probe lock: %v", err)
	}
	if !granted.Valid || granted.Int64 != 1 {
		t.Fatalf("probe could not take the journal-writer lock (granted=%v)", granted)
	}
	defer func() { _, _ = probe.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName) }()
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := store.SetMetadata(wctx, "nojournal-key", "v"); err != nil {
		t.Fatalf("NoJournal write queued or failed behind the admission lock: %v", err)
	}
	if n := journalRows(); n != 1 {
		t.Fatalf("journal rows = %d after a NoJournal write, want still 1", n)
	}

	// Journal-off retention: with the journal off the funnel keeps the no-op.
	store.SetEventsJournalEnabled(false)
	if err := store.withRetryTxNoJournal(ctx, func(tx *sql.Tx) error {
		return issueops.RecordDeleteInTx(ctx, tx, "bd-off", "actor")
	}); err != nil {
		t.Fatalf("journal-off NoJournal emit is not a no-op: %v", err)
	}
}

// TestJournalWriterLock_BusyAdmissionNamesTheError pins AC-5 of
// gastownhall/beads#6117: a waiter whose admission budget expires while
// another writer holds the journal-writer lock gets the named
// ErrJournalWriterBusy — never an unbounded queue, never a generic timeout —
// and the HTTP surface maps that sentinel to a retryable 503 busy with the
// contention Retry-After (problem_test.go). The waiter here admits through
// the same issueops.AcquireJournalWriterLock the write funnels call, with an
// admission deadline of the same shape withRetryTxOn fixes (an absolute
// budget, earlier than the session's own context deadline, so the BUDGET is
// what expires — a waiter whose own ctx dies mid-wait keeps its cancellation
// error, as the helper documents).
func TestJournalWriterLock_BusyAdmissionNamesTheError(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	defer store.SetEventsJournalEnabled(false)

	// A competing holder on the database's journal-writer lock.
	probe, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin probe connection: %v", err)
	}
	defer func() { _ = probe.Close() }()
	lockName := issueops.JournalWriterLockName(store.database)
	var granted sql.NullInt64
	if err := probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted); err != nil {
		t.Fatalf("take probe lock: %v", err)
	}
	if !granted.Valid || granted.Int64 != 1 {
		t.Fatalf("probe could not take the journal-writer lock (granted=%v)", granted)
	}
	defer func() { _, _ = probe.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName) }()

	// The waiter: a pinned session with a live context, admitting on an
	// absolute budget the holder outlasts. The answer is the named busy
	// error, and the session is handed back clean (a busy acquisition is
	// certain — no discard needed).
	waiter, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin waiter connection: %v", err)
	}
	defer func() { _ = waiter.Close() }()
	wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
	defer wcancel()
	_, err = issueops.AcquireJournalWriterLock(wctx, waiter, store.database,
		time.Now().Add(2*time.Second), store.poolReadDeadline())
	if !errors.Is(err, issueops.ErrJournalWriterBusy) {
		t.Fatalf("admission behind a held journal lock = %v, want ErrJournalWriterBusy", err)
	}
}
