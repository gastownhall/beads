package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The proxied plumbing's half of the journal writer lock (#6117): units of
// work that may commit bead writes queue on the same server-side lock as the
// dolt store's writers, events journal on or off, and nothing else does.
// sqlmock fails any statement it was not told to expect, so a test that
// expects no GET_LOCK proves none was sent. A RELEASE_LOCK it did not expect
// fails, and the failed release discards the session, so "exactly one release"
// is asserted as "the session went back to the pool" on every path that pools
// it.

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
// route, journal on or off: a re-arm records a journal event, so RunTxScoped
// queues like RunTx, even for an attempt that names no tables and takes the
// plain COMMIT.
func TestJournalWriterLock_RunTxScopedTakesAndReleases(t *testing.T) {
	for _, journal := range []bool{true, false} {
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			p, mock := newJournaledMockTxProvider(t)
			p.SetEventsJournalEnabled(journal)
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
		})
	}
}

// TestJournalWriterLock_OnlyCommittingUnitsLock pins who does NOT queue,
// journal on or off: reads (RunTxRead and the direct NewUOW readers) and the
// ephemeral metadata/prune writes, which write no bead image table. A
// committing unit of work queues with the journal off too: a bead write that
// skipped the lock could land inside another's read-then-write window on the
// same bead.
func TestJournalWriterLock_OnlyCommittingUnitsLock(t *testing.T) {
	ctx := context.Background()
	noop := func(ctx context.Context, uw UnitOfWork) (struct{}, error) { return struct{}{}, nil }

	for _, journal := range []bool{true, false} {
		newProvider := func(t *testing.T) (*doltSQLProvider, sqlmock.Sqlmock) {
			p, mock := newJournaledMockTxProvider(t)
			p.SetEventsJournalEnabled(journal)
			return p, mock
		}
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			t.Run("RunTxRead", func(t *testing.T) {
				p, mock := newProvider(t)
				mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
				_, err := RunTxRead(ctx, p, noop)
				require.NoError(t, err)
				require.NoError(t, mock.ExpectationsWereMet())
			})
			t.Run("RunTxEphemeral", func(t *testing.T) {
				p, mock := newProvider(t)
				mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
				_, err := RunTxEphemeral(ctx, p, noop)
				require.NoError(t, err)
				require.NoError(t, mock.ExpectationsWereMet())
			})
			t.Run("direct NewUOW", func(t *testing.T) {
				p, mock := newProvider(t)
				mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
				uw, err := p.NewUOW(ctx)
				require.NoError(t, err)
				uw.Close(ctx)
				require.NoError(t, mock.ExpectationsWereMet())
			})
			t.Run("RunTx locks", func(t *testing.T) {
				p, mock := newProvider(t)
				expectJournalLock(mock)
				mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
				expectJournalUnlock(mock)
				require.NoError(t, RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) { return "", nil }))
				require.NoError(t, mock.ExpectationsWereMet())
				assert.Equal(t, 1, p.db.Stats().OpenConnections, "a confirmed release pools the session")
			})
		})
	}
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

// TestJournalWriterLock_ReleasedBeforeTheRecheck pins the order on a live
// server: a committed unit of work frees the journal writer lock before its
// post-commit blocked recheck, so no journaled writer queues behind the
// recheck's retries. The unit of work runs on a pool whose sessions probe the
// lock from another session at the recheck's START TRANSACTION, the first
// statement after the commit on the pinned session.
func TestJournalWriterLock_ReleasedBeforeTheRecheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const prefix = "jwr"
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	kit := newUOWRoleFixtureKit(provider, prefix)
	p, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "provider %T is not a *doltSQLProvider", provider)

	blocker, dependent := prefix+"-blk", prefix+"-dep"
	for _, id := range []string{blocker, dependent} {
		require.NoError(t, kit.CreateIssue(ctx, &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}, "seeder"))
	}
	require.NoError(t, kit.AddDependency(ctx, &types.Dependency{IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks}, "seeder"))
	requireBlocked(t, ctx, kit, dependent, true)

	name := issueops.JournalWriterLockName(p.database)
	var (
		probed   bool
		got      sql.NullInt64
		probeErr error
	)
	probe := func() {
		probed = true
		conn, err := p.db.Conn(ctx)
		if err != nil {
			probeErr = err
			return
		}
		defer func() { _ = conn.Close() }()
		if probeErr = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&got); probeErr == nil && got.Int64 == 1 {
			_, probeErr = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", name)
		}
	}

	host, port, err := net.SplitHostPort(strings.TrimPrefix(p.serverEndpoint, "tcp:"))
	require.NoError(t, err)
	portNum, err := strconv.Atoi(port)
	require.NoError(t, err)
	dsn := buildDSN(proxy.Endpoint{Host: host, Port: portNum}, p.database, "root", "", "")
	pool := sql.OpenDB(recheckProbeConnector{dsn: dsn, drv: p.db.Driver(), probe: probe})
	t.Cleanup(func() { _ = pool.Close() })
	probing := &doltSQLProvider{defaultBranch: p.defaultBranch, db: pool, database: p.database}
	probing.SetEventsJournalEnabled(true)

	require.NoError(t, RunTx(ctx, probing, func(ctx context.Context, uw UnitOfWork) (string, error) {
		_, err := uw.IssueUseCase().CloseIssue(ctx, blocker, domain.CloseIssueParams{Reason: "done"}, "closer")
		return "bd: close " + blocker, err
	}))

	require.True(t, probed, "the close recorded no post-commit recheck, so nothing probed the lock")
	require.NoError(t, probeErr)
	assert.Equal(t, int64(1), got.Int64, "another session could not take %s during the recheck: the lock is still held", name)
	requireBlocked(t, ctx, kit, dependent, false)
}

// recheckProbeConnector opens dsn behind a recheckProbeConn that runs probe
// once, at the first START TRANSACTION after a COMMIT on its session.
type recheckProbeConnector struct {
	dsn   string
	drv   driver.Driver
	probe func()
}

func (c recheckProbeConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &recheckProbeConn{Conn: conn, probe: c.probe}, nil
}

func (c recheckProbeConnector) Driver() driver.Driver { return c.drv }

// recheckProbeConn keeps the optional interfaces the mysql connection
// implements so database/sql takes the same paths it takes over a bare one.
type recheckProbeConn struct {
	driver.Conn
	probe     func()
	committed bool
	probed    bool
}

func (c *recheckProbeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.Contains(strings.ToUpper(query), "COMMIT"):
		c.committed = true
	case c.committed && !c.probed && strings.HasPrefix(query, "START TRANSACTION"):
		c.probed = true
		c.probe()
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *recheckProbeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *recheckProbeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *recheckProbeConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *recheckProbeConn) CheckNamedValue(nv *driver.NamedValue) error {
	return c.Conn.(driver.NamedValueChecker).CheckNamedValue(nv)
}
