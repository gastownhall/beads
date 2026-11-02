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
	publicops "github.com/steveyegge/beads/issueops"
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

// TestJournalWriterLock_CleanupOrderBeforeUnlock pins AC-6 of
// gastownhall/beads#6117: on every exit path of an admitted write — success,
// body error, panic, cancellation — the transaction is ended (committed or
// rolled back) BEFORE the admission lock and the pinned session go, so the
// lock never outlives an open transaction and a session never returns to the
// pool holding one. The cleanup order itself is AC-1's code; these are its
// retention guards, holding at base and after. A follow-up probe session
// taking the lock at once is the observable: released; a quiet pool is the
// session back (or discarded, which the sql package accounts for the same
// way).
func TestJournalWriterLock_CleanupOrderBeforeUnlock(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	defer store.SetEventsJournalEnabled(false)

	lockName := issueops.JournalWriterLockName(store.database)

	// lockFree asserts the admission lock is free: a fresh session takes it
	// at once and gives it back. It must hold whenever an admitted write has
	// RETURNED — whatever the exit path.
	lockFree := func() {
		t.Helper()
		c, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin probe connection: %v", err)
		}
		defer func() { _ = c.Close() }()
		var granted sql.NullInt64
		if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted); err != nil {
			t.Fatalf("probe GET_LOCK: %v", err)
		}
		if !granted.Valid || granted.Int64 != 1 {
			t.Fatalf("admission lock not free after the write returned (granted=%v)", granted)
		}
		_, _ = c.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName)
	}
	// sessionsQuiet asserts no store-pinned session is still out of the
	// pool: released, or discarded (which closes it).
	sessionsQuiet := func() {
		t.Helper()
		if inUse := store.db.Stats().InUse; inUse != 0 {
			t.Fatalf("pool InUse = %d after the write returned, want 0", inUse)
		}
	}

	newIssue := func(title string) *types.Issue {
		return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	issueStatus := func(id string) types.Status {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("get issue %s: %v", id, err)
		}
		return issue.Status
	}

	t.Run("success commits and hands the lock back", func(t *testing.T) {
		target := newIssue("cleanup success")
		if err := store.CreateIssue(ctx, target, "actor"); err != nil {
			t.Fatalf("create target issue: %v", err)
		}
		if err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
			_, err := issueops.UpdateIssueInTx(ctx, tx, target.ID, map[string]interface{}{"status": types.StatusInProgress}, "actor-b")
			return err
		}); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := issueStatus(target.ID); got != types.StatusInProgress {
			t.Fatalf("status = %q, want the committed in_progress", got)
		}
		lockFree()
		sessionsQuiet()
	})

	t.Run("body error rolls back before unlock", func(t *testing.T) {
		target := newIssue("cleanup error")
		if err := store.CreateIssue(ctx, target, "actor"); err != nil {
			t.Fatalf("create target issue: %v", err)
		}
		boom := errors.New("cleanup error test")
		if err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
			if _, err := issueops.UpdateIssueInTx(ctx, tx, target.ID, map[string]interface{}{"status": types.StatusInProgress}, "actor-b"); err != nil {
				return err
			}
			return boom
		}); err == nil || !errors.Is(err, boom) {
			t.Fatalf("write error = %v, want the body's own", err)
		}
		if got := issueStatus(target.ID); got != types.StatusOpen {
			t.Fatalf("status = %q, want the rolled-back open", got)
		}
		lockFree()
		sessionsQuiet()
	})

	t.Run("panic rolls back before unlock and propagates", func(t *testing.T) {
		target := newIssue("cleanup panic")
		if err := store.CreateIssue(ctx, target, "actor"); err != nil {
			t.Fatalf("create target issue: %v", err)
		}
		func() {
			defer func() {
				if r := recover(); r != "cleanup panic test" {
					t.Fatalf("panic value = %v, want it propagated", r)
				}
			}()
			_ = store.withRetryTx(ctx, func(tx *sql.Tx) error {
				if _, err := issueops.UpdateIssueInTx(ctx, tx, target.ID, map[string]interface{}{"status": types.StatusInProgress}, "actor-b"); err != nil {
					return err
				}
				panic("cleanup panic test")
			})
		}()
		if got := issueStatus(target.ID); got != types.StatusOpen {
			t.Fatalf("status = %q, want the rolled-back open", got)
		}
		lockFree()
		sessionsQuiet()
	})

	t.Run("cancellation rolls back before unlock, never replayed", func(t *testing.T) {
		target := newIssue("cleanup cancel")
		if err := store.CreateIssue(ctx, target, "actor"); err != nil {
			t.Fatalf("create target issue: %v", err)
		}
		bctx, bcancel := context.WithCancel(ctx)
		runs := 0
		err := store.withRetryTx(bctx, func(tx *sql.Tx) error {
			runs++
			if _, err := issueops.UpdateIssueInTx(bctx, tx, target.ID, map[string]interface{}{"status": types.StatusInProgress}, "actor-b"); err != nil {
				return err
			}
			bcancel()
			<-bctx.Done()
			return bctx.Err()
		})
		if err == nil {
			t.Fatalf("canceled write returned nil")
		}
		if runs != 1 {
			t.Fatalf("canceled body ran %d times, want exactly 1 (never replayed)", runs)
		}
		if got := issueStatus(target.ID); got != types.StatusOpen {
			t.Fatalf("status = %q, want the rolled-back open", got)
		}
		lockFree()
		sessionsQuiet()
	})

	t.Run("raw site returns its session after the commit", func(t *testing.T) {
		target := newIssue("cleanup raw site")
		if err := store.CreateIssue(ctx, target, "actor"); err != nil {
			t.Fatalf("create target issue: %v", err)
		}
		if err := store.AddComment(ctx, target.ID, "actor", "cleanup-order probe"); err != nil {
			t.Fatalf("add comment: %v", err)
		}
		lockFree()
		sessionsQuiet()
	})
}

// TestJournalWriterLock_FencedDeleteContention pins AC-7 of
// gastownhall/beads#6117: a guarded delete runs under the per-id delete fence
// on ONE pinned session, and when the journal is on, that same session — not
// a second store-pool connection — must take the journal-writer admission
// lock (fence first, journal second, same session). The test store's pool is
// capped at two connections, and the probe holds one, so the fenced delete
// has exactly one available: if admission wrongly pinned another pool
// connection it could never be granted, and both arms below would hang to
// their context deadline instead of resolving.
func TestJournalWriterLock_FencedDeleteContention(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	defer store.SetEventsJournalEnabled(false)

	lockName := issueops.JournalWriterLockName(store.database)
	newIssue := func(title string) *types.Issue {
		issue := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "actor"); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		stored, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("read back %s: %v", issue.ID, err)
		}
		return stored
	}
	deleteJournalRow := func(op, id string) (string, bool) {
		var got string
		err := store.db.QueryRowContext(ctx,
			`SELECT actor FROM bd_events_journal WHERE op = ? AND issue_id = ? ORDER BY seq DESC LIMIT 1`,
			op, id).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false
		}
		if err != nil {
			t.Fatalf("query journal %s/%s: %v", op, id, err)
		}
		return got, true
	}
	// fenceFree asserts the delete fence for id was handed back: a fresh
	// session takes it at once and gives it back.
	fenceFree := func(id string) {
		t.Helper()
		c, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin fence probe connection: %v", err)
		}
		defer func() { _ = c.Close() }()
		var granted sql.NullInt64
		if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", deleteFenceLockName(store.database, id)).Scan(&granted); err != nil {
			t.Fatalf("probe fence lock: %v", err)
		}
		if !granted.Valid || granted.Int64 != 1 {
			t.Fatalf("delete fence for %s still held after the delete returned (granted=%v)", id, granted)
		}
		_, _ = c.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", deleteFenceLockName(store.database, id))
	}
	guardedDelete := func(ctx context.Context, id string, rowVersion int64) error {
		deleter, err := store.Deleter()
		if err != nil {
			t.Fatalf("Deleter(): %v", err)
		}
		_, err = deleter.Delete(ctx, publicops.DeleteRequest{
			IDs: []string{id}, ExpectedVersion: &rowVersion, Actor: "fence-test",
		})
		return err
	}

	t.Run("queues on the fence session, then proceeds", func(t *testing.T) {
		target := newIssue("fenced delete queues")

		// The competitor: one pool session holds the journal-writer lock,
		// leaving exactly one connection of the capped pool for the fenced
		// delete — its fence session.
		probe, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin probe connection: %v", err)
		}
		defer func() { _ = probe.Close() }()
		var granted sql.NullInt64
		if err := probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted); err != nil {
			t.Fatalf("take probe lock: %v", err)
		}
		if !granted.Valid || granted.Int64 != 1 {
			t.Fatalf("probe could not take the journal-writer lock (granted=%v)", granted)
		}
		releaseProbe := func() {
			_, _ = probe.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName)
		}
		defer releaseProbe()

		type outcome struct {
			err error
			elapsed time.Duration
		}
		done := make(chan outcome, 1)
		start := time.Now()
		go func() {
			err := guardedDelete(ctx, target.ID, target.RowVersion)
			done <- outcome{err: err, elapsed: time.Since(start)}
		}()

		// The fenced delete has pinned its fence session (InUse = probe +
		// fence session) and is queued on the admission lock. If admission
		// needed a second store-pool connection, the pool (capped at 2, one
		// held by the probe) could never grant it and the delete below would
		// never finish.
		deadline := time.Now().Add(10 * time.Second)
		for store.db.Stats().InUse < 2 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if inUse := store.db.Stats().InUse; inUse != 2 {
			t.Fatalf("pool InUse = %d while the fenced delete queued, want 2 (probe + fence session)", inUse)
		}
		// Give the queued admission its first GET_LOCK slice, then hand the
		// lock over: the fenced delete enters, deletes, and commits on its
		// own session.
		time.Sleep(300 * time.Millisecond)
		releaseProbe()

		select {
		case res := <-done:
			if res.err != nil {
				t.Fatalf("fenced delete under short journal contention: %v", res.err)
			}
			if res.elapsed > 20*time.Second {
				t.Fatalf("fenced delete took %v, want a queue-then-proceed, not a hang", res.elapsed)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("fenced delete never finished after the probe released the journal lock")
		}

		if _, err := store.GetIssue(ctx, target.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("get deleted issue = %v, want ErrNotFound", err)
		}
		if actor, ok := deleteJournalRow("delete", target.ID); !ok || actor != "fence-test" {
			t.Fatalf("delete journal row actor = %q, %v; want fence-test — the delete did not mint through the admitted path", actor, ok)
		}
		fenceFree(target.ID)
	})

	t.Run("contention past the admission budget is busy, fence handed back", func(t *testing.T) {
		target := newIssue("fenced delete busy")

		probe, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin probe connection: %v", err)
		}
		defer func() { _ = probe.Close() }()
		var granted sql.NullInt64
		if err := probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted); err != nil {
			t.Fatalf("take probe lock: %v", err)
		}
		if !granted.Valid || granted.Int64 != 1 {
			t.Fatalf("probe could not take the journal-writer lock (granted=%v)", granted)
		}
		// Held past the delete's return: contention beyond the whole
		// server-mode admission budget, the shape the busy outcome exists
		// for. The ctx below is the 90s test budget, strictly beyond the 15s
		// admission deadline, so the failure is the named busy error rather
		// than a cancellation racing the same deadline.
		defer func() {
			_, _ = probe.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName)
		}()

		start := time.Now()
		err = guardedDelete(ctx, target.ID, target.RowVersion)
		elapsed := time.Since(start)
		if !errors.Is(err, issueops.ErrJournalWriterBusy) {
			t.Fatalf("fenced delete under sustained contention = %v, want ErrJournalWriterBusy", err)
		}
		if elapsed < 14*time.Second || elapsed > 30*time.Second {
			t.Fatalf("fenced delete failed after %v, want the ~15s admission budget (not an instant error, not the 20s fence timeout, not a hang)", elapsed)
		}
		// The body never ran: the row is intact and no journal row was minted.
		stored, err := store.GetIssue(ctx, target.ID)
		if err != nil {
			t.Fatalf("get issue after the refused delete: %v", err)
		}
		if stored.Status != types.StatusOpen {
			t.Fatalf("status = %q after the refused delete, want open", stored.Status)
		}
		if _, ok := deleteJournalRow("delete", target.ID); ok {
			t.Fatalf("the refused delete minted a journal row")
		}
		// The fence went back with the failed write, and the pinned session
		// was released or discarded: only the probe is still out.
		fenceFree(target.ID)
		if inUse := store.db.Stats().InUse; inUse != 1 {
			t.Fatalf("pool InUse = %d after the refused delete, want 1 (the probe only)", inUse)
		}
	})
}
