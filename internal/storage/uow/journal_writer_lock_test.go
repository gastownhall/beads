package uow

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestJournalWriterLock_UOWRouteAdmission pins AC-2 of
// gastownhall/beads#6117 for the uow side: with the journal on, a unit of work
// on a write route (RunTxResultWithin, RunTxScoped, or an unmarked NewUOW)
// takes the database's journal-writer admission lock on its transaction
// session BEFORE START TRANSACTION, so it queues while another writer holds
// the lock; a read or ephemeral route (RunTxRead, RunTxEphemeral) carries the
// explicit unlocked marker and begins at once. The probe session takes the
// same lock by name (issueops.JournalWriterLockName), the one the direct store
// takes too, so this is the cross-provider queue: any admitted writer on the
// database, store or uow, holds this test's writer out of BEGIN.
func TestJournalWriterLock_UOWRouteAdmission(t *testing.T) {
	provider := newTestUOWProvider(t)
	p := provider.(*doltSQLProvider)
	p.SetEventsJournalEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The harness (openTestUOWProvider) boots the provider against database
	// "beads", so the admission lock its write routes queue on is
	// bd_events_seq:beads.
	lockName := issueops.JournalWriterLockName("beads")

	// holdLock takes the journal-writer lock on a probe session of the
	// provider's own pool, standing in for any other admitted writer on the
	// database. The returned release is idempotent.
	holdLock := func(t *testing.T) func() {
		t.Helper()
		probe, err := p.db.Conn(ctx)
		require.NoError(t, err)
		var granted sql.NullInt64
		require.NoError(t, probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted))
		require.True(t, granted.Valid, "GET_LOCK returned NULL")
		require.EqualValues(t, 1, granted.Int64, "probe session must take the journal-writer lock")
		var once sync.Once
		return func() {
			once.Do(func() {
				_, _ = probe.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName)
				_ = probe.Close()
			})
		}
	}
	// waitFor asserts cond within wait, failing with msg when it does not.
	waitFor := func(t *testing.T, wait time.Duration, msg string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(wait)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("%s", msg)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	t.Run("RunTxResultWithin queues behind the lock", func(t *testing.T) {
		release := holdLock(t)
		defer release()

		started := make(chan struct{}, 1)
		done := make(chan error, 1)
		go func() {
			_, err := RunTxResultWithin(ctx, provider, 15*time.Second, func(ctx context.Context, uw UnitOfWork) (struct{}, string, error) {
				started <- struct{}{}
				return struct{}{}, "", nil
			})
			done <- err
		}()
		// While the lock is held the attempt must not BEGIN its transaction:
		// the body never runs.
		select {
		case <-started:
			t.Fatalf("RunTxResultWithin began its transaction while another writer held the journal lock")
		case <-time.After(1500 * time.Millisecond):
		}
		release()
		waitFor(t, 10*time.Second, "RunTxResultWithin never began after the lock was released", func() bool {
			select {
			case <-started:
				return true
			default:
				return false
			}
		})
		require.NoError(t, <-done)
	})

	t.Run("RunTxScoped queues behind the lock", func(t *testing.T) {
		release := holdLock(t)
		defer release()

		started := make(chan struct{}, 1)
		done := make(chan error, 1)
		go func() {
			_, err := RunTxScoped(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, []string, string, error) {
				started <- struct{}{}
				return struct{}{}, nil, "", nil
			})
			done <- err
		}()
		select {
		case <-started:
			t.Fatalf("RunTxScoped began its transaction while another writer held the journal lock")
		case <-time.After(1500 * time.Millisecond):
		}
		release()
		waitFor(t, 10*time.Second, "RunTxScoped never began after the lock was released", func() bool {
			select {
			case <-started:
				return true
			default:
				return false
			}
		})
		require.NoError(t, <-done)
	})

	t.Run("unmarked NewUOW is conservatively admitted", func(t *testing.T) {
		release := holdLock(t)
		defer release()

		type attempt struct {
			uw  UnitOfWork
			err error
		}
		got := make(chan attempt, 1)
		go func() {
			uw, err := provider.NewUOW(ctx)
			got <- attempt{uw, err}
		}()
		select {
		case a := <-got:
			t.Fatalf("unmarked NewUOW began while the journal lock was held (err=%v)", a.err)
		case <-time.After(1500 * time.Millisecond):
		}
		release()
		var a attempt
		require.Eventually(t, func() bool {
			select {
			case a = <-got:
				return true
			default:
				return false
			}
		}, 10*time.Second, 20*time.Millisecond, "unmarked NewUOW never began after the lock was released")
		require.NoError(t, a.err)
		a.uw.Close(ctx)
	})

	t.Run("RunTxRead does not queue", func(t *testing.T) {
		release := holdLock(t)
		defer release()

		started := make(chan struct{}, 1)
		_, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
			started <- struct{}{}
			return struct{}{}, nil
		})
		require.NoError(t, err, "a read route must complete while another writer holds the journal lock")
		select {
		case <-started:
		default:
			t.Fatalf("RunTxRead returned without running its body")
		}
	})

	t.Run("RunTxEphemeral does not queue", func(t *testing.T) {
		release := holdLock(t)
		defer release()

		started := make(chan struct{}, 1)
		_, err := RunTxEphemeral(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
			started <- struct{}{}
			return struct{}{}, nil
		})
		require.NoError(t, err, "an ephemeral route must complete while another writer holds the journal lock")
		select {
		case <-started:
		default:
			t.Fatalf("RunTxEphemeral returned without running its body")
		}
	})
}
// TestJournalWriterLock_DisabledJournalUOWTakesNoLock pins AC-3 of
// gastownhall/beads#6117 for the uow provider: with the journal off, no unit
// of work takes the journal-writer admission lock — a write route begins and
// completes promptly while another session HOLDS the lock.
func TestJournalWriterLock_DisabledJournalUOWTakesNoLock(t *testing.T) {
	provider := newTestUOWProvider(t)
	// The provider's journal stays OFF — the harness default.

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	lockName := issueops.JournalWriterLockName("beads")
	probe, err := provider.(*doltSQLProvider).db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = probe.Close() }()
	var granted sql.NullInt64
	require.NoError(t, probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted))
	require.True(t, granted.Valid, "GET_LOCK returned NULL")
	require.EqualValues(t, 1, granted.Int64, "probe session must take the journal-writer lock")
	defer func() { _, _ = probe.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName) }()

	started := make(chan struct{}, 1)
	_, err = RunTxResultWithin(ctx, provider, 15*time.Second, func(ctx context.Context, uw UnitOfWork) (struct{}, string, error) {
		started <- struct{}{}
		return struct{}{}, "", nil
	})
	require.NoError(t, err, "a journal-off write route must complete while another writer holds the journal lock")
	select {
	case <-started:
	default:
		t.Fatalf("RunTxResultWithin returned without running its body")
	}
}

// TestJournalUnlockedScope_EphemeralRouteCannotEmit pins AC-4 of
// gastownhall/beads#6117 for the uow side: an ephemeral unit of work (no
// journal-writer admission lock) with the journal ON is scoped
// journal-FORBIDDEN — an emit attempt inside its body is refused before any
// sequence is allocated, and even a callback that ignores the emit error
// cannot commit it. On the unfixed provider the ephemeral route silently
// JOURNALS from outside the serialized writers: the emit succeeds and its
// row commits.
func TestJournalUnlockedScope_EphemeralRouteCannotEmit(t *testing.T) {
	provider := newTestUOWProvider(t)
	p := provider.(*doltSQLProvider)
	p.SetEventsJournalEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	journalRows := func() int {
		var seqs []int64
		rows, err := p.db.QueryContext(ctx, "SELECT seq FROM bd_events_journal ORDER BY seq")
		require.NoError(t, err)
		for rows.Next() {
			var seq int64
			require.NoError(t, rows.Scan(&seq))
			seqs = append(seqs, seq)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		return len(seqs)
	}

	_, err := RunTxEphemeral(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
		// The body's runner is the pinned session itself (doltServerTx.Runner
		// is the *sql.Conn), so this emit runs on the transaction's own
		// session — the way an IssueUseCase mutation would.
		dbtx, ok := uw.(*baseUOW).tx.Runner().(issueops.DBTX)
		require.True(t, ok, "the pinned session runner must carry the DBTX interface")
		_ = issueops.RecordDeleteInTx(ctx, dbtx, "bd-ephemeral", "actor") // ignored: the commit must refuse it
		return struct{}{}, nil
	})
	require.Error(t, err, "an ephemeral unit of work that attempted a journal emit must fail, not commit it")
	if n := journalRows(); n != 0 {
		t.Fatalf("journal rows = %d after a refused ephemeral emit, want 0", n)
	}
}

// TestJournalUnlockedScope_ReadRouteCannotEmit pins AC-4 of
// gastownhall/beads#6117 for the uow read route: a read unit of work with the
// journal ON is scoped journal-FORBIDDEN, so an emit attempt inside its body
// fails the read rather than journaling from outside the serialized writers.
func TestJournalUnlockedScope_ReadRouteCannotEmit(t *testing.T) {
	provider := newTestUOWProvider(t)
	p := provider.(*doltSQLProvider)
	p.SetEventsJournalEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
		// The body's runner is the pinned session itself (doltServerTx.Runner
		// is the *sql.Conn), so this emit runs on the transaction's own
		// session — the way an IssueUseCase mutation would.
		dbtx, ok := uw.(*baseUOW).tx.Runner().(issueops.DBTX)
		require.True(t, ok, "the pinned session runner must carry the DBTX interface")
		return struct{}{}, issueops.RecordDeleteInTx(ctx, dbtx, "bd-read", "actor")
	})
	require.Error(t, err, "a read unit of work that attempted a journal emit must fail, not journal silently")
}
