package uow

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// The proxied plumbing's half of the journal writer lock (#6117): units of
// work that may commit journaled writes queue on the same server-side lock as
// the dolt store's writers, and nothing else does. sqlmock fails any statement
// it was not told to expect, so a test that expects no GET_LOCK proves none
// was sent. A RELEASE_LOCK it did not expect fails, and the failed release
// discards the session, so "exactly one release" is asserted as "the session
// went back to the pool" on every path that pools it.

var (
	matchGetLock     = regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")
	matchReleaseLock = regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")
)

func newJournaledMockTxProvider(t *testing.T) (*doltSQLProvider, sqlmock.Sqlmock) {
	t.Helper()
	p, mock := newMockTxProvider(t)
	p.database = "proj_a"
	p.SetEventsJournalEnabled(true)
	return p, mock
}

func expectJournalLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(matchGetLock).
		WithArgs(issueops.JournalWriterLockName("proj_a"), issueops.JournalWriterLockWait).
		WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
}

func expectJournalUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(matchReleaseLock).
		WithArgs(issueops.JournalWriterLockName("proj_a")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestJournalWriterLock_SharesTheStoreLockName(t *testing.T) {
	assert.Equal(t, "bd_events_seq:proj_a", issueops.JournalWriterLockName("proj_a"),
		"the proxied and direct writers must queue on one name per database")
}

// TestJournalWriterLock_RunTxCommitTakesAndReleases covers the commit path:
// GET_LOCK before START TRANSACTION, one RELEASE_LOCK after the commit, and
// the session pooled.
func TestJournalWriterLock_RunTxCommitTakesAndReleases(t *testing.T) {
	p, mock := newJournaledMockTxProvider(t)
	expectJournalLock(mock)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 0)
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalUnlock(mock)

	err := RunTx(context.Background(), p, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: write", nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "a confirmed release pools the session")
}

// TestJournalWriterLock_RunTxResultRollbackReleases covers the rollback path:
// a RunTxResult whose work commits nothing still held the lock, and releases
// it once after the ROLLBACK.
func TestJournalWriterLock_RunTxResultRollbackReleases(t *testing.T) {
	p, mock := newJournaledMockTxProvider(t)
	expectJournalLock(mock)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalUnlock(mock)

	got, err := RunTxResult(context.Background(), p, func(ctx context.Context, uw UnitOfWork) (int, string, error) {
		return 7, "", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 7, got)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "a confirmed release pools the session")
}

// TestJournalWriterLock_SerializationRetryReleasesEachAttempt covers the
// retry path: the attempt that loses a 1213 releases its lock when its unit
// of work closes, and the replay takes and releases its own.
func TestJournalWriterLock_SerializationRetryReleasesEachAttempt(t *testing.T) {
	p, mock := newJournaledMockTxProvider(t)
	expectJournalLock(mock)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 1)
	mock.ExpectExec("DOLT_COMMIT").WillReturnError(&mysql.MySQLError{Number: 1213, Message: "serialization failure"})
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalUnlock(mock)
	expectJournalLock(mock)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 1)
	mock.ExpectExec("DOLT_COMMIT").WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalUnlock(mock)

	attempts := 0
	err := RunTx(context.Background(), p, func(ctx context.Context, uw UnitOfWork) (string, error) {
		attempts++
		return "bd: write", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "both releases confirmed: the session is pooled")
}

// TestJournalWriterLock_PoisonPathReleasesOnce covers the poison path: a
// failed commit whose rollback also fails releases the lock once before the
// session is discarded.
func TestJournalWriterLock_PoisonPathReleasesOnce(t *testing.T) {
	p, mock := newJournaledMockTxProvider(t)
	expectJournalLock(mock)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 1)
	mock.ExpectExec("DOLT_COMMIT").WillReturnError(errors.New("commit exploded"))
	mock.ExpectExec("ROLLBACK").WillReturnError(errors.New("rollback exploded too"))
	expectJournalUnlock(mock)

	tx, err := p.BeginTx(withJournalWriter(context.Background()))
	require.NoError(t, err)
	require.ErrorContains(t, tx.Commit(context.Background(), "msg"), "commit exploded")
	require.NoError(t, tx.Rollback(context.Background()), "a second close is a no-op")
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 0, p.db.Stats().OpenConnections, "a poisoned session is discarded")
}

// TestJournalWriterLock_RunTxScopedTakesAndReleases covers the heartbeat
// route: a re-arm records a journal event, so RunTxScoped queues like RunTx,
// even for an attempt that names no tables and takes the plain COMMIT.
func TestJournalWriterLock_RunTxScopedTakesAndReleases(t *testing.T) {
	p, mock := newJournaledMockTxProvider(t)
	expectJournalLock(mock)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalUnlock(mock)

	_, err := RunTxScoped(context.Background(), p, func(ctx context.Context, uw UnitOfWork) (struct{}, []string, string, error) {
		return struct{}{}, nil, "", nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "a confirmed release pools the session")
}

// TestJournalWriterLock_OnlyCommittingUnitsLock pins who does NOT queue:
// reads (RunTxRead and the direct NewUOW readers), the ephemeral
// metadata/prune writes, and every unit of work when the journal is off.
func TestJournalWriterLock_OnlyCommittingUnitsLock(t *testing.T) {
	ctx := context.Background()
	noop := func(ctx context.Context, uw UnitOfWork) (struct{}, error) { return struct{}{}, nil }

	t.Run("RunTxRead", func(t *testing.T) {
		p, mock := newJournaledMockTxProvider(t)
		mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
		_, err := RunTxRead(ctx, p, noop)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("RunTxEphemeral", func(t *testing.T) {
		p, mock := newJournaledMockTxProvider(t)
		mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
		_, err := RunTxEphemeral(ctx, p, noop)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("direct NewUOW", func(t *testing.T) {
		p, mock := newJournaledMockTxProvider(t)
		mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
		uw, err := p.NewUOW(ctx)
		require.NoError(t, err)
		uw.Close(ctx)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("RunTx with the journal off", func(t *testing.T) {
		p, mock := newMockTxProvider(t)
		p.database = "proj_a"
		mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
		require.NoError(t, RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) { return "", nil }))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// TestJournalWriterLock_FailedUnlockDiscardsSession pins that a RELEASE_LOCK
// the server refuses must not return a session that may still own the
// (reentrant, per-session) lock to the pool.
func TestJournalWriterLock_FailedUnlockDiscardsSession(t *testing.T) {
	p, mock := newMockTxProvider(t)
	p.database = "review"
	p.eventsJournalEnabled.Store(true)
	mock.ExpectQuery("SELECT GET_LOCK").WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SELECT RELEASE_LOCK").WillReturnError(&mysql.MySQLError{Number: 1317, Message: "Query execution was interrupted"})

	tx, err := p.BeginTx(withJournalWriter(context.Background()))
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
	if got := p.db.Stats().OpenConnections; got != 0 {
		t.Fatalf("failed RELEASE_LOCK returned %d live session to pool; want discard", got)
	}
}

// TestJournalWriterLock_UnlockHasCleanupDeadline pins that the detached unlock
// must not outlive the cleanup budget when the server stalls, even after the
// caller's context is cancelled.
func TestJournalWriterLock_UnlockHasCleanupDeadline(t *testing.T) {
	p, mock := newMockTxProvider(t)
	conn, err := p.db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	mock.ExpectQuery("SELECT GET_LOCK").WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
	mock.ExpectExec("SELECT RELEASE_LOCK").WillDelayFor(6 * time.Second).WillReturnResult(sqlmock.NewResult(0, 0))

	ctx, cancel := context.WithCancel(context.Background())
	unlock, err := lockJournalWriter(ctx, conn, "review")
	require.NoError(t, err)
	cancel()
	start := time.Now()
	unlock()
	if elapsed := time.Since(start); elapsed > txCloseTimeout+500*time.Millisecond {
		t.Fatalf("unlock after cancellation took %s, exceeding %s cleanup budget", elapsed, txCloseTimeout)
	}
	assert.Equal(t, 0, p.db.Stats().OpenConnections, "an unconfirmed release discards the session")
}
