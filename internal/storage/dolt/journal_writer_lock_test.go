package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/journalscan"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/memoryops"
)

var (
	matchGetLock     = regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")
	matchReleaseLock = regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")
	matchUsedLock    = regexp.QuoteMeta("SELECT IS_USED_LOCK(?)")
	matchHolderState = regexp.QuoteMeta("SELECT COMMAND, TIME FROM information_schema.PROCESSLIST WHERE ID = ?")
)

// expectHolderGone has a timed-out GET_LOCK find no holder to reclaim: the
// lock was released between the two statements.
func expectHolderGone(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(matchUsedLock).WillReturnRows(sqlmock.NewRows([]string{"holder"}).AddRow(nil))
}

// newJournalLockMockStore is a server-free store over sqlmock. sqlmock fails
// any statement it was not told to expect, so a test that sets no expectation
// proves the helper issued no SQL at all.
func newJournalLockMockStore(t *testing.T, serverMode, journal bool) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db, database: "proj_a", serverMode: serverMode}
	store.eventsJournalEnabled.Store(journal)
	return store, mock
}

// sqlRecorder is the statement log of a recording mock store. sqlmock checks
// only that the statements it was told to expect arrived; one it was NOT told
// to expect comes back as an error and is forgotten, and reclaimSilentHolder
// swallows its errors by design. A test that must prove a statement was never
// sent — a KILL of a live writer, a holder probe past the attempt bound —
// asserts on this log instead, which sees every statement whatever sqlmock
// answered.
type sqlRecorder struct {
	mu    sync.Mutex
	stmts []string
}

func (r *sqlRecorder) record(query string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stmts = append(r.stmts, query)
}

// matching returns the recorded statements that start with prefix, in order.
func (r *sqlRecorder) matching(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, stmt := range r.stmts {
		if strings.HasPrefix(stmt, prefix) {
			out = append(out, stmt)
		}
	}
	return out
}

// sawAfter reports whether a statement starting with next was recorded after
// the first one starting with first.
func (r *sqlRecorder) sawAfter(first, next string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := false
	for _, stmt := range r.stmts {
		switch {
		case !seen:
			seen = strings.HasPrefix(stmt, first)
		case strings.HasPrefix(stmt, next):
			return true
		}
	}
	return false
}

// recordingConnector hands database/sql the connection for dsn — sqlmock's,
// or a live server's — behind a recordingConn. With a gate, its sessions also
// hold their journaled commits there (see commitGate).
type recordingConnector struct {
	dsn  string
	drv  driver.Driver
	rec  *sqlRecorder
	gate *commitGate
}

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return recordingConn{Conn: conn, rec: c.rec, gate: c.gate, journaled: new(bool)}, nil
}

func (c recordingConnector) Driver() driver.Driver { return c.drv }

// recordingConn logs each statement before passing it to sqlmock, and keeps
// the optional interfaces sqlmock implements so database/sql takes the same
// paths it takes over a bare sqlmock connection.
type recordingConn struct {
	driver.Conn
	rec  *sqlRecorder
	gate *commitGate
	// journaled is whether the session's open transaction has advanced
	// bd_events_seq; database/sql runs one statement at a time on a session.
	journaled *bool
}

func (c recordingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.record(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c recordingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.record(query)
	if strings.HasPrefix(query, "UPDATE bd_events_seq SET next_seq = next_seq + 1") {
		*c.journaled = true
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c recordingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	*c.journaled = false
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil || c.gate == nil {
		return tx, err
	}
	return gatedTx{Tx: tx, conn: c}, nil
}

// gatedTx holds its COMMIT at the session's gate when the transaction has
// advanced bd_events_seq.
type gatedTx struct {
	driver.Tx
	conn recordingConn
}

func (tx gatedTx) Commit() error {
	if *tx.conn.journaled {
		tx.conn.gate.hold()
	}
	return tx.Tx.Commit()
}

// commitGate holds one journaled commit — the first COMMIT, after the gate is
// armed, of a transaction that advanced bd_events_seq — until the test lets it
// go, so the test can look at the server while that writer sits between its
// journaled writes and its commit.
type commitGate struct {
	mu     sync.Mutex
	held   chan struct{}
	resume chan struct{}
}

// arm sets the gate for the next journaled commit. held is closed once that
// commit is waiting at the gate; resume lets it go and is idempotent.
func (g *commitGate) arm() (held <-chan struct{}, resume func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held, g.resume = make(chan struct{}), make(chan struct{})
	release := g.resume
	return g.held, sync.OnceFunc(func() { close(release) })
}

func (g *commitGate) hold() {
	g.mu.Lock()
	held, resume := g.held, g.resume
	g.held, g.resume = nil, nil
	g.mu.Unlock()
	if held == nil {
		return
	}
	close(held)
	<-resume
}

func (c recordingConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c recordingConn) CheckNamedValue(nv *driver.NamedValue) error {
	return c.Conn.(driver.NamedValueChecker).CheckNamedValue(nv)
}

// newRecordingJournalLockMockStore is newJournalLockMockStore for a journaled
// server store whose statements are also logged in the returned recorder.
func newRecordingJournalLockMockStore(t *testing.T) (*DoltStore, sqlmock.Sqlmock, *sqlRecorder) {
	t.Helper()
	dsn := "journal-lock-recording:" + t.Name()
	mockDB, mock, err := sqlmock.NewWithDSN(dsn, sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.NewWithDSN: %v", err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })
	rec := &sqlRecorder{}
	db := sql.OpenDB(recordingConnector{dsn: dsn, drv: mockDB.Driver(), rec: rec})
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db, database: "proj_a", serverMode: true}
	store.eventsJournalEnabled.Store(true)
	return store, mock, rec
}

// TestWriteTxConn_NoLockInEmbeddedMode pins that the journal writer lock costs
// nothing where there is no server to share: an embedded store pins the
// connection and issues no GET_LOCK, journal on or off. A server store locks
// with the journal off too (TestWriteTxConn_JournalWriterLockCarriesDatabase).
func TestWriteTxConn_NoLockInEmbeddedMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		journal bool
	}{
		{"journal on", true},
		{"journal off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newJournalLockMockStore(t, false, tc.journal)
			conn, release, err := store.writeTxConn(context.Background())
			if err != nil {
				t.Fatalf("writeTxConn: %v", err)
			}
			if conn == nil {
				t.Fatal("writeTxConn returned no connection")
			}
			release()
			release()
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected SQL: %v", err)
			}
		})
	}
}

// TestWriteTxConn_JournalWriterLockCarriesDatabase pins the lock's name and
// lifecycle in server mode, events journal on or off: GET_LOCK on the
// database's name before the caller begins, and exactly one RELEASE_LOCK
// however often release runs.
func TestWriteTxConn_JournalWriterLockCarriesDatabase(t *testing.T) {
	for _, journal := range []bool{true, false} {
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			store, mock := newJournalLockMockStore(t, true, journal)
			mock.ExpectQuery(matchGetLock).
				WithArgs("bd_events_seq:proj_a", issueops.JournalWriterLockWait).
				WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
			mock.ExpectExec(matchReleaseLock).
				WithArgs("bd_events_seq:proj_a").
				WillReturnResult(sqlmock.NewResult(0, 0))

			_, release, err := store.writeTxConn(context.Background())
			if err != nil {
				t.Fatalf("writeTxConn: %v", err)
			}
			release()
			release()
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("lock lifecycle: %v", err)
			}
			if got := store.db.Stats().Idle; got != 1 {
				t.Fatalf("a confirmed release left %d idle sessions, want the session pooled (1)", got)
			}
		})
	}
}

// TestWriteTxConn_FailedReleaseDiscardsSession pins that a RELEASE_LOCK the
// server refuses does not hand the session back to the pool: locks are per
// session and reentrant, so a pooled session that may still own the lock
// would keep every other journaled writer out.
func TestWriteTxConn_FailedReleaseDiscardsSession(t *testing.T) {
	store, mock := newJournalLockMockStore(t, true, true)
	mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
	mock.ExpectExec(matchReleaseLock).
		WillReturnError(&mysql.MySQLError{Number: 1317, Message: "Query execution was interrupted"})

	_, release, err := store.writeTxConn(context.Background())
	if err != nil {
		t.Fatalf("writeTxConn: %v", err)
	}
	release()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("lock lifecycle: %v", err)
	}
	if got := store.db.Stats().OpenConnections; got != 0 {
		t.Fatalf("failed RELEASE_LOCK returned %d live session(s) to the pool; want the session discarded", got)
	}
}

// TestWriteTxConn_ReleaseIsBounded pins the cleanup deadline: release runs
// detached from the caller's (here already cancelled) context, but a server
// that stalls on RELEASE_LOCK must not hold the caller past
// issueops.JournalWriterUnlockTimeout, and the unconfirmed session is
// discarded.
func TestWriteTxConn_ReleaseIsBounded(t *testing.T) {
	store, mock := newJournalLockMockStore(t, true, true)
	mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
	mock.ExpectExec(matchReleaseLock).WillDelayFor(6 * time.Second).WillReturnResult(sqlmock.NewResult(0, 0))

	ctx, cancel := context.WithCancel(context.Background())
	_, release, err := store.writeTxConn(ctx)
	if err != nil {
		t.Fatalf("writeTxConn: %v", err)
	}
	cancel()
	start := time.Now()
	release()
	if elapsed := time.Since(start); elapsed > issueops.JournalWriterUnlockTimeout+500*time.Millisecond {
		t.Fatalf("release after cancellation took %s, beyond the %s cleanup budget", elapsed, issueops.JournalWriterUnlockTimeout)
	}
	if got := store.db.Stats().OpenConnections; got != 0 {
		t.Fatalf("a timed-out RELEASE_LOCK returned %d live session(s) to the pool; want the session discarded", got)
	}
}

// TestJournalWriterLock_DeleteFenceLocksTheFencedSession pins the lock order
// for a guarded delete in server mode with the journal on: withDeleteFence
// takes its fence on a pinned session, and the write it runs takes the journal
// writer lock on that same session, after the fence. The journal lock is
// released after COMMIT and before the fence, so a fenced delete never holds
// the journal lock while it waits for a fence.
func TestJournalWriterLock_DeleteFenceLocksTheFencedSession(t *testing.T) {
	store, mock := newJournalLockMockStore(t, true, true)
	fence := deleteFenceLockName("proj_a", "bd-1")
	mock.ExpectQuery(matchGetLock).
		WithArgs(fence, deleteFenceLockTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectQuery(matchGetLock).
		WithArgs("bd_events_seq:proj_a", issueops.JournalWriterLockWait).
		WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectCommit()
	mock.ExpectExec(matchReleaseLock).
		WithArgs("bd_events_seq:proj_a").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(matchReleaseLock).
		WithArgs(fence).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	if err := store.withDeleteFence(context.Background(), "bd-1", func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("withDeleteFence: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("fence and journal lock order: %v", err)
	}
	if got := store.db.Stats().OpenConnections; got != 1 {
		t.Fatalf("a fenced delete used %d session(s), want the fence and the journal lock on one", got)
	}
}

// TestCommitWriteTx_PanicRollsBackAndPropagates pins that a write body that
// panics surfaces its panic instead of hanging in the deferred release:
// database/sql's Conn.Close waits for an open Tx to finish, so the transaction
// must be rolled back before the pinned session is released — locked or not.
func TestCommitWriteTx_PanicRollsBackAndPropagates(t *testing.T) {
	for _, tc := range []struct {
		name string
		lock writeLock
	}{
		{"locked", journaledWrite},
		{"unlocked", unjournaledWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newJournalLockMockStore(t, true, true)
			if tc.lock == journaledWrite {
				mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
			}
			mock.ExpectBegin()
			mock.ExpectRollback()
			if tc.lock == journaledWrite {
				mock.ExpectExec(matchReleaseLock).WillReturnResult(sqlmock.NewResult(0, 0))
			}

			recovered := make(chan any, 1)
			go func() {
				defer func() { recovered <- recover() }()
				_, _ = store.commitWriteTxWith(context.Background(), store.db, tc.lock, func(*sql.Tx) error {
					panic("write body panic")
				})
			}()
			select {
			case got := <-recovered:
				if got != "write body panic" {
					t.Fatalf("recovered %v, want the write body's panic", got)
				}
			case <-time.After(time.Second):
				t.Fatal("a panicking write body hung in the deferred connection release instead of propagating")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("panic path SQL: %v", err)
			}
			if got := store.db.Stats().InUse; got != 0 {
				t.Fatalf("a panicking write body left %d session(s) in use, want the session released", got)
			}
		})
	}
}

// TestWithRetryTxNoJournal_EmitFailsAndCommitsNothing pins the runtime half of
// the NoJournal guard: a journal emit inside a NoJournal body returns
// issueops.ErrJournalUnlockedTx before any SQL — no seq allocated on
// bd_events_seq outside the writer lock, no journal row — and the write rolls
// back without a retry. sqlmock fails the statements it was not told to expect.
func TestWithRetryTxNoJournal_EmitFailsAndCommitsNothing(t *testing.T) {
	for _, journal := range []bool{true, false} {
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			store, mock := newJournalLockMockStore(t, true, journal)
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("UPDATE config SET")).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectRollback()

			calls := 0
			err := store.withRetryTxNoJournal(context.Background(), func(tx *sql.Tx) error {
				calls++
				if _, err := tx.ExecContext(context.Background(), "UPDATE config SET value = 'x'"); err != nil {
					return err
				}
				return issueops.RecordEventInTx(context.Background(), tx, issueops.EventUpdate, "bd-nj-emit", "actor")
			})
			if !errors.Is(err, issueops.ErrJournalUnlockedTx) {
				t.Fatalf("emit in a NoJournal body = %v, want issueops.ErrJournalUnlockedTx", err)
			}
			if calls != 1 {
				t.Fatalf("NoJournal body ran %d times, want once: the emit failure must not be retried", calls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("NoJournal emit SQL: %v", err)
			}
		})
	}
}

// TestHeartbeatIssue_TakesTheJournalWriterLock pins that a lease heartbeat
// queues behind bead writers in server mode, journal on or off: a re-arm
// records a lease_rearmed event and a journal entry (RenewLeaseInTx), so the
// heartbeat cannot run on a NoJournal helper, and a plain refresh takes the
// same route.
func TestHeartbeatIssue_TakesTheJournalWriterLock(t *testing.T) {
	for _, journal := range []bool{true, false} {
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			store, mock := newJournalLockMockStore(t, true, journal)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ?")).
				WillReturnRows(sqlmock.NewRows([]string{"1"}))
			mock.ExpectQuery(matchGetLock).
				WithArgs("bd_events_seq:proj_a", issueops.JournalWriterLockWait).
				WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
			mock.ExpectBegin()
			mock.ExpectExec("UPDATE leases SET lease_expires_at").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM issues WHERE id = ?")).
				WillReturnRows(sqlmock.NewRows([]string{"live"}).AddRow(1))
			mock.ExpectCommit()
			mock.ExpectExec(matchReleaseLock).
				WithArgs("bd_events_seq:proj_a").
				WillReturnResult(sqlmock.NewResult(0, 0))

			if err := store.HeartbeatIssue(context.Background(), "bd-hb-1", "worker"); err != nil {
				t.Fatalf("HeartbeatIssue: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("heartbeat SQL: %v", err)
			}
		})
	}
}

// TestWriteTxConn_JournalWriterLockRetriesThenGivesUp pins the bounded wait:
// a GET_LOCK that times out (0) or errors server-side (NULL) is retried, and
// after issueops.JournalWriterLockAttempts the helper returns an error naming the lock
// and the total wait instead of proceeding unlocked.
func TestWriteTxConn_JournalWriterLockRetriesThenGivesUp(t *testing.T) {
	t.Run("granted on a later attempt", func(t *testing.T) {
		store, mock := newJournalLockMockStore(t, true, true)
		mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(0))
		expectHolderGone(mock)
		mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(nil))
		mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
		mock.ExpectExec(matchReleaseLock).WillReturnResult(sqlmock.NewResult(0, 0))

		_, release, err := store.writeTxConn(context.Background())
		if err != nil {
			t.Fatalf("writeTxConn: %v", err)
		}
		release()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("lock retries: %v", err)
		}
	})

	t.Run("exhausted", func(t *testing.T) {
		store, mock, rec := newRecordingJournalLockMockStore(t)
		// The last timed-out attempt probes no holder: a reclaim there would
		// buy an attempt past the bound. The probe's refusal by sqlmock is
		// swallowed, so the recorder, not the expectations, proves its absence.
		for i := 0; i < issueops.JournalWriterLockAttempts; i++ {
			mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(0))
			if i < issueops.JournalWriterLockAttempts-1 {
				expectHolderGone(mock)
			}
		}

		conn, release, err := store.writeTxConn(context.Background())
		if err == nil {
			release()
			t.Fatal("writeTxConn succeeded without the lock")
		}
		if conn != nil || release != nil {
			t.Error("a failed writeTxConn must hand back neither a connection nor a release")
		}
		for _, want := range []string{"bd_events_seq:proj_a", "60s"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("lock attempts: %v", err)
		}
		if probes := rec.matching("SELECT IS_USED_LOCK"); len(probes) != issueops.JournalWriterLockAttempts-1 {
			t.Errorf("%d holder probes, want %d: none after the last attempt", len(probes), issueops.JournalWriterLockAttempts-1)
		}
	})
}

// TestWriteTxConn_ReclaimsOnlyASilentHolder pins which holders a waiter ends
// after a timed-out GET_LOCK: a session in Sleep past
// issueops.JournalWriterStaleHolder is killed and the next attempt is granted;
// one running a statement, or silent for less, is left alone and the waiter
// keeps waiting. A KILL sqlmock was not told to expect is refused, but the
// refusal is swallowed like any reclaim failure, so the recorder is what
// proves no KILL was sent.
func TestWriteTxConn_ReclaimsOnlyASilentHolder(t *testing.T) {
	stale := int64(issueops.JournalWriterStaleHolder / time.Second)
	for _, tc := range []struct {
		name    string
		command string
		idle    int64
		kill    bool
	}{
		{"silent past the bound", "Sleep", stale, true},
		{"silent under the bound", "Sleep", stale - 1, false},
		{"running a statement", "Query", stale * 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, rec := newRecordingJournalLockMockStore(t)
			mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(0))
			mock.ExpectQuery(matchUsedLock).WillReturnRows(sqlmock.NewRows([]string{"holder"}).AddRow(1279845))
			mock.ExpectQuery(matchHolderState).WithArgs(int64(1279845)).
				WillReturnRows(sqlmock.NewRows([]string{"command", "time"}).AddRow(tc.command, tc.idle))
			if tc.kill {
				mock.ExpectExec(regexp.QuoteMeta("KILL 1279845")).WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectQuery(matchGetLock).WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
			mock.ExpectExec(matchReleaseLock).WillReturnResult(sqlmock.NewResult(0, 0))

			_, release, err := store.writeTxConn(context.Background())
			if err != nil {
				t.Fatalf("writeTxConn: %v", err)
			}
			release()
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("holder reclaim: %v", err)
			}
			var want []string
			if tc.kill {
				want = []string{"KILL 1279845"}
			}
			if kills := rec.matching("KILL"); !slices.Equal(kills, want) {
				t.Errorf("KILL statements sent = %q, want %q", kills, want)
			}
		})
	}
}

// TestJournalWriterLock_SilentHolderIsReclaimed reproduces a lost client on a
// live server: a session takes the journal writer lock and then sends nothing,
// which is all the server sees of a client that froze mid-write without closing
// its socket. A journaled write behind it must land within the stale-holder
// bound plus one GET_LOCK wait, and the silent session must be gone.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestJournalWriterLock_SilentHolderIsReclaimed(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)
	if err := store.CreateIssue(ctx, &types.Issue{ID: "bd-sh-1", Title: "sh", IssueType: types.TypeTask, Status: types.StatusOpen}, "actor"); err != nil {
		t.Fatalf("create: %v", err)
	}
	shortenStaleHolder(t, 2*time.Second)

	holder := openSecondStoreOnBranch(t, ctx, store)
	conn, err := holder.db.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer conn.Close()
	var got int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", issueops.JournalWriterLockName(store.database)).Scan(&got); err != nil || got != 1 {
		t.Fatalf("hold journal writer lock: got=%d err=%v", got, err)
	}

	bound := issueops.JournalWriterStaleHolder + 2*time.Duration(issueops.JournalWriterLockWait)*time.Second
	callCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	start := time.Now()
	err = store.UpdateIssue(callCtx, "bd-sh-1", map[string]interface{}{"title": "reclaimed"}, "actor")
	t.Logf("journaled UpdateIssue behind a silent holder: %s (err=%v)", time.Since(start), err)
	if err != nil {
		t.Fatalf("a silent lock holder blocked a journaled write past %s: %v", bound, err)
	}
	if err := conn.PingContext(ctx); err == nil {
		t.Error("the silent holder's session survived the reclaim")
	}
}

// staleHolderMu serializes the live tests that shorten
// issueops.JournalWriterStaleHolder: they run in parallel, and one's restore
// landing inside the other's window would leave it waiting on the full bound.
var staleHolderMu sync.Mutex

// shortenStaleHolder sets issueops.JournalWriterStaleHolder to d for the rest
// of the test.
func shortenStaleHolder(t *testing.T, d time.Duration) {
	t.Helper()
	staleHolderMu.Lock()
	stale := issueops.JournalWriterStaleHolder
	issueops.JournalWriterStaleHolder = d
	t.Cleanup(func() {
		issueops.JournalWriterStaleHolder = stale
		staleHolderMu.Unlock()
	})
}

// TestJournalWriterLock_AgedHolderIsNotReclaimed pins what the reclaim keys
// on: the time since the holder's last statement (PROCESSLIST TIME on a Sleep
// row), not the age of its connection. The holder keeps the lock, sending a
// statement every half second, until the waiter's statement log shows a
// reclaim probe (the PROCESSLIST read after a timed-out GET_LOCK) and the
// GET_LOCK after it, so the reclaim has looked at the holder and decided. By
// then the holder's session is older than the stale bound. It must still be
// alive, the waiter must have sent no KILL, and the waiter's write must land
// once the holder lets go.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it watches.
func TestJournalWriterLock_AgedHolderIsNotReclaimed(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := store.CreateIssue(ctx, &types.Issue{ID: "bd-ah-1", Title: "ah", IssueType: types.TypeTask, Status: types.StatusOpen}, "actor"); err != nil {
		t.Fatalf("create: %v", err)
	}
	shortenStaleHolder(t, 2*time.Second)
	rec := &sqlRecorder{}
	waiter := openRecordingStoreOnBranch(t, ctx, store, rec, nil)
	enableJournalForTest(t, waiter)

	holder := openSecondStoreOnBranch(t, ctx, store)
	conn, err := holder.db.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer conn.Close()
	lockName := issueops.JournalWriterLockName(store.database)
	var got int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", lockName).Scan(&got); err != nil || got != 1 {
		t.Fatalf("hold journal writer lock: got=%d err=%v", got, err)
	}
	held := time.Now()

	// Every wait below is bounded, and a failure lets go of the lock and
	// drains the waiter before the stores close.
	bound := 3 * time.Duration(issueops.JournalWriterLockWait) * time.Second
	releaseLock := sync.OnceFunc(func() { _, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName) })
	done := make(chan error, 1)
	waited := sync.OnceValue(func() error {
		select {
		case err := <-done:
			return err
		case <-time.After(bound):
			return fmt.Errorf("no result within %s", bound)
		}
	})
	defer func() {
		releaseLock()
		_ = waited()
	}()
	go func() {
		done <- waiter.UpdateIssue(ctx, "bd-ah-1", map[string]interface{}{"title": "after the aged holder"}, "actor")
	}()

	const probe = "SELECT COMMAND, TIME FROM information_schema.PROCESSLIST"
	for !rec.sawAfter(probe, "SELECT GET_LOCK") {
		if time.Since(held) > bound {
			t.Fatalf("the waiter sent no reclaim probe and asked again within %s of the holder taking the lock", bound)
		}
		time.Sleep(500 * time.Millisecond)
		if _, err := conn.ExecContext(ctx, "SELECT 1"); err != nil {
			t.Fatalf("the holder's session was ended %s into its hold, though it was never silent for %s: %v",
				time.Since(held).Round(time.Millisecond), issueops.JournalWriterStaleHolder, err)
		}
	}
	t.Logf("reclaim probe observed %s into the hold", time.Since(held).Round(time.Millisecond))
	if _, err := conn.ExecContext(ctx, "SELECT 1"); err != nil {
		t.Fatalf("the reclaim probe ended a live holder whose connection was merely older than %s: %v", issueops.JournalWriterStaleHolder, err)
	}
	if kills := rec.matching("KILL"); len(kills) != 0 {
		t.Errorf("the waiter sent %q at a holder that was never silent for %s", kills, issueops.JournalWriterStaleHolder)
	}
	releaseLock()
	if err := waited(); err != nil {
		t.Fatalf("journaled UpdateIssue behind a live holder: %v", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		t.Errorf("the holder's session did not survive the reclaim probe: %v", err)
	}
}

// TestJournalWriterLock_EveryJournalScopeTakesTheLock is the structural guard
// for the lock's coverage. The lock queues journaled writers only if EVERY
// transaction that can write the journal takes it, and a site that forgets
// still works — it just goes back to aborting its peers with 1213. So every
// function that binds journal activation to a transaction it began must take
// the lock in the same body, through writeTxConn or writeTxSession or, on a
// connection it pinned itself, lockJournalWriter.
func TestJournalWriterLock_EveryJournalScopeTakesTheLock(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}
	lockCalls := map[string]bool{"writeTxConn": true, "writeTxSession": true, "lockJournalWriter": true}
	var checked int
	for key, f := range fns {
		// scopeEventsJournalTransaction is the scoping helper itself; its
		// callers are what this checks.
		if key == "DoltStore.scopeEventsJournalTransaction" || !f.CallsAnyOf(scopeCalls) {
			continue
		}
		checked++
		if !f.CallsAnyOf(lockCalls) {
			t.Errorf("%s scopes the events journal on a transaction but never takes the journal writer lock — "+
				"its journaled writes abort overlapping writers with 1213 again. Begin it on s.writeTxConn (see wisps.go).", key)
		}
	}
	if checked == 0 {
		t.Fatal("guard found no journal-scoping functions — scope detection changed; the guard is not actually running")
	}
}

// journalingIssueopsMutators derives, from the issueops source, every
// exported function that reaches a journal emit helper — the same derivation
// TestEveryRawTxJournalScopeIsScopedOrExempt makes.
func journalingIssueopsMutators(t *testing.T) map[string]bool {
	t.Helper()
	issueFns, err := journalscan.ParsePackage("../issueops")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}
	emitHelpers := map[string]bool{
		"RecordEventInTx":        true,
		"RecordDeleteInTx":       true,
		"RecordDepEventInTx":     true,
		"RecordCommentEventInTx": true,
		"insertEventRow":         true,
	}
	emits := journalscan.Fixpoint(issueFns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(emitHelpers) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })
	mutators := map[string]bool{}
	for key, f := range issueFns {
		if f.Exported && emits[key] {
			mutators[f.Name] = true
		}
	}
	if len(mutators) == 0 {
		t.Fatal("derived no journaling issueops mutators — the emit analysis changed and this guard is not actually running")
	}
	return mutators
}

// imageWriteExempt are the issueops functions whose own SQL reads as a bead
// image-table write to sqlWritesImageTable but is not one, each with why. Only
// their own SQL is exempt: what they call is still followed.
var imageWriteExempt = map[string]string{
	"GetNextChildIDTx": "its templated INSERT INTO %s is child_counters or wisp_child_counters",
}

// eventTableWrite matches DML against the two event tables of a bead's image,
// which journalscan.BeadTables leaves out.
var eventTableWrite = regexp.MustCompile("(?i)\\b(INSERT\\s+(IGNORE\\s+)?INTO|REPLACE\\s+INTO|UPDATE|DELETE\\s+FROM)\\s+`?(wisp_)?events\\b")

// sqlWritesImageTable reports whether a SQL string literal writes one of a
// bead's image tables (issueops.JournalWriterLockTables): journalscan's bead
// table detector, which also reads a templated %s table as one, plus the event
// tables.
func sqlWritesImageTable(lit string) bool {
	return journalscan.SQLWritesBeadTable(lit) || eventTableWrite.MatchString(lit)
}

// imageWritingIssueopsFuncs derives, from the issueops source, every exported
// function that writes a bead image table, in its own SQL or through any
// number of issueops helpers (journalscan.Fixpoint, by bare name).
func imageWritingIssueopsFuncs(t *testing.T) map[string]bool {
	t.Helper()
	issueFns, err := journalscan.ParsePackage("../issueops")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}
	own := ownImageDML(t, "../issueops")
	for name := range imageWriteExempt {
		if !own[name] {
			t.Fatalf("imageWriteExempt names %s, whose own SQL no longer reads as an image-table write — drop the exemption", name)
		}
		delete(own, name)
	}
	writes := journalscan.Fixpoint(issueFns,
		func(f *journalscan.FuncInfo) bool {
			if f.Recv != "" {
				return own[f.Recv+"."+f.Name]
			}
			return own[f.Name]
		},
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })
	out := map[string]bool{}
	for key, f := range issueFns {
		if f.Exported && writes[key] {
			out[f.Name] = true
		}
	}
	// Writers the derivation must find: compaction rewrites the issues row but
	// records no journal event, so only this rule keeps it locked.
	for _, want := range []string{"ApplyCompactionInTx", "RestoreFromSnapshotInTx"} {
		if !out[want] {
			t.Fatalf("derived image-table writers %v lack %s — the DML detection changed and this guard is not actually running", out, want)
		}
	}
	return out
}

// ownImageDML reports, per function of the package in dir keyed like
// journalscan, whether its own body holds SQL that writes a bead image table.
func ownImageDML(t *testing.T, dir string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	out := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && bodyWritesImageTable(fn.Body) {
					out[funcDeclKey(fn)] = true
				}
			}
		}
	}
	return out
}

// bodyWritesImageTable reports whether body, closures included, holds a SQL
// string literal that writes a bead image table.
func bodyWritesImageTable(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && sqlWritesImageTable(lit.Value) {
			found = true
		}
		return !found
	})
	return found
}

// lockedOnlyOps is every issueops and memoryops function a body must take the
// journal writer lock to call: the journaling mutators and the image-table
// writers.
func lockedOnlyOps(t *testing.T) map[string]bool {
	t.Helper()
	ops := journalingIssueopsMutators(t)
	for name := range imageWritingIssueopsFuncs(t) {
		ops[name] = true
	}
	memoryopsReaching(t, ops)
	return ops
}

// memoryopsReaching extends ops with every exported function of the storage
// memoryops package that reaches one: memories.Remember and Forget reach
// issueops only through it.
func memoryopsReaching(t *testing.T, mutators map[string]bool) {
	t.Helper()
	memFns, err := journalscan.ParsePackage("../memoryops")
	if err != nil {
		t.Fatalf("parse memoryops package: %v", err)
	}
	emits := journalscan.Fixpoint(memFns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(mutators) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })
	for key, f := range memFns {
		if f.Exported && emits[key] {
			mutators[f.Name] = true
		}
	}
}

// noJournalCallers are the callers the audits put on the NoJournal helpers,
// each with the tables it writes, none of a bead's image. Each must stay where
// the converse guard looks: one that drops off the list has either moved back
// to the locked route or been renamed past the guard.
var noJournalCallers = []string{
	"DoltStore.SetConfig",              // config
	"DoltStore.DeleteConfig",           // config
	"DoltStore.SetMetadata",            // metadata
	"DoltStore.SetLocalMetadata",       // local_metadata
	"DoltStore.PruneEventsJournal",     // bd_events_journal
	"DoltStore.RunEventsMaintenanceTx", // bd_events_journal
	"DoltStore.SetRepoMtime",           // repo_mtimes
	"DoltStore.ClearRepoMtime",         // repo_mtimes
	"DoltStore.GetNextChildID",         // child_counters, wisp_child_counters
	"DoltStore.RecordProvenanceEvent",  // provenance_events
	"memories.Remember",                // config
	"memories.Forget",                  // config
	"bootstrapper.Bootstrap",           // config, metadata
}

// lockedNotNoJournal are the callers moved off the NoJournal helpers because
// they write a bead image table; the guard must not find them back.
var lockedNotNoJournal = []string{
	"DoltStore.ApplyCompaction",
	"DoltStore.SnapshotIssue",
	"DoltStore.RestoreFromSnapshot",
}

// TestJournalWriterLock_NoJournalBodiesRecordNothing is the converse guard:
// the NoJournal transaction helpers skip the lock, which is only safe for a
// body that can never advance bd_events_seq and writes none of a bead's image
// tables. A journaled write slipped into one would go back to aborting its
// peers with 1213 — or being aborted — and an image-table write would land
// inside another writer's read-then-write window on the same bead. So no
// function that calls withRetryTxNoJournal/withWriteTxNoJournal may write an
// image table in its own SQL, or call a journaling or image-writing issueops
// function, or a function of this package, on any receiver, that reaches one
// through any number of helpers. See
// parseDoltCallGraph for how calls resolve and noJournalViolations for the one
// edge the reach does not follow.
func TestJournalWriterLock_NoJournalBodiesRecordNothing(t *testing.T) {
	checked, violations := noJournalViolations(t, parseDoltCallGraph(t), lockedOnlyOps(t))
	for _, v := range violations {
		t.Error(v)
	}
	for _, want := range noJournalCallers {
		if !checked[want] {
			t.Errorf("NoJournal callers = %v, want %s among them — the guard is not looking where it should", checked, want)
		}
	}
	for _, moved := range lockedNotNoJournal {
		if checked[moved] {
			t.Errorf("%s runs on a NoJournal transaction helper again; it writes a bead image table and must take the journal writer lock", moved)
		}
	}
}

// noJournalGuardFixture is a NoJournal body on a non-DoltStore receiver that
// reaches a journaling mutator through a helper on the same receiver — the
// case a DoltStore-only reach would miss. It is parsed into the
// package's call graph only, never compiled.
const noJournalGuardFixture = `package dolt

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (m *memories) fixtureNoJournalProbe(ctx context.Context) error {
	return m.store.withRetryTxNoJournal(ctx, func(tx *sql.Tx) error {
		return m.fixtureHopOne(ctx, tx)
	})
}

func (m *memories) fixtureHopOne(ctx context.Context, tx *sql.Tx) error {
	_, err := issueops.ReclaimExpiredLeasesInTx(ctx, tx, time.Now(), types.ReclaimFilter{}, "fixture")
	return err
}
`

// noJournalImageFixture holds NoJournal bodies that record no journal event
// but write a bead image table: one through a helper that calls a
// non-journaling issueops writer (compaction is a documented journal gap),
// one through a helper's own SQL, and one in the body's own closure.
const noJournalImageFixture = `package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func (m *memories) fixtureImageProbe(ctx context.Context) error {
	return m.store.withRetryTxNoJournal(ctx, func(tx *sql.Tx) error {
		return m.fixtureCompactHop(ctx, tx)
	})
}

func (m *memories) fixtureCompactHop(ctx context.Context, tx *sql.Tx) error {
	return issueops.ApplyCompactionInTx(ctx, tx, "bd-fx", 1, 0, "")
}

func (s *DoltStore) fixtureRawImageProbe(ctx context.Context) error {
	return s.withWriteTxNoJournal(ctx, func(tx *sql.Tx) error {
		return fixtureRawLabelHop(ctx, tx)
	})
}

func fixtureRawLabelHop(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO labels (issue_id, label) VALUES (?, ?)", "bd-fx", "x")
	return err
}

func (s *DoltStore) fixtureOwnImageProbe(ctx context.Context) error {
	return s.withRetryTxNoJournal(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM wisp_events WHERE issue_id = ?", "bd-fx")
		return err
	})
}
`

// TestJournalWriterLock_NoJournalGuardReachesEveryReceiver pins the guard's
// reach past DoltStore and its two rules: the journaling fixture's helper is a
// memories method, and the image fixture's bodies journal nothing but write a
// bead image table; the guard must name each.
func TestJournalWriterLock_NoJournalGuardReachesEveryReceiver(t *testing.T) {
	ops := lockedOnlyOps(t)
	for _, tc := range []struct {
		name    string
		fixture string
		want    []string
	}{
		{"journaling helper", noJournalGuardFixture, []string{
			"memories.fixtureNoJournalProbe runs on a NoJournal transaction helper but calls memories.fixtureHopOne,",
		}},
		{"image-table writes", noJournalImageFixture, []string{
			"DoltStore.fixtureOwnImageProbe runs on a NoJournal transaction helper but writes a bead image table in its own SQL",
			"DoltStore.fixtureRawImageProbe runs on a NoJournal transaction helper but calls fixtureRawLabelHop,",
			"memories.fixtureImageProbe runs on a NoJournal transaction helper but calls memories.fixtureCompactHop,",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, violations := noJournalViolations(t, parseDoltCallGraph(t, tc.fixture), ops)
			if len(violations) != len(tc.want) {
				t.Fatalf("violations = %q, want %d starting %q", violations, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.HasPrefix(violations[i], want) {
					t.Errorf("violation %d = %q, want it to start %q", i, violations[i], want)
				}
			}
		})
	}
}

// noJournalViolations reports every function that runs on a NoJournal
// transaction helper and writes a bead image table in its own SQL, or calls
// one of ops (lockedOnlyOps) or a package function that reaches one or writes
// an image table itself, with the callers it checked.
//
// The reach follows every call but one: an edge into recheckBlockedAfterCommit.
// That post-commit recheck journals on a locked transaction of its own
// (withRetryTx), opened after the caller's transaction has committed, so it
// never writes on the unlocked one; following it would mark retryWriteTx, and
// through it every NoJournal helper.
func noJournalViolations(t *testing.T, g doltCallGraph, mutators map[string]bool) (map[string]bool, []string) {
	t.Helper()
	const postCommitRecheck = "DoltStore.recheckBlockedAfterCommit"
	reaches := map[string]bool{}
	for key, f := range g {
		if f.ownImageDML || f.callsAnyOf(mutators) {
			reaches[key] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for key, f := range g {
			if reaches[key] {
				continue
			}
			for callee := range f.callees {
				if callee != postCommitRecheck && reaches[callee] {
					reaches[key], changed = true, true
					break
				}
			}
		}
	}
	for _, helper := range []string{"withRetryTxNoJournal", "withWriteTxNoJournal", "retryWriteTx", "commitWriteTx", "commitWriteTxWith"} {
		if reaches["DoltStore."+helper] {
			t.Fatalf("the transaction plumbing %s reads as journaling — the reach analysis is following a call it should not, and would flag every NoJournal caller", helper)
		}
	}
	noJournal := map[string]bool{"withRetryTxNoJournal": true, "withWriteTxNoJournal": true}
	checked := map[string]bool{}
	var violations []string
	for key, f := range g {
		if !f.callsAnyOf(noJournal) {
			continue
		}
		checked[key] = true
		if f.ownImageDML {
			violations = append(violations, fmt.Sprintf("%s runs on a NoJournal transaction helper but writes a bead image table in its own SQL — use withRetryTx/withWriteTx so it takes the journal writer lock", key))
		}
		var bad []string
		for _, c := range f.calls {
			if mutators[c] {
				bad = append(bad, c)
			}
		}
		for callee := range f.callees {
			if callee != postCommitRecheck && reaches[callee] {
				bad = append(bad, callee)
			}
		}
		for _, c := range bad {
			violations = append(violations, fmt.Sprintf("%s runs on a NoJournal transaction helper but calls %s, which can record a journal event or write a bead image table — use withRetryTx/withWriteTx so it takes the journal writer lock", key, c))
		}
	}
	sort.Strings(violations)
	return checked, violations
}

// doltCallGraph is this package's non-test functions, keyed like journalscan
// ("Recv.Name", or "Name" for a free function).
type doltCallGraph map[string]*doltFunc

type doltFunc struct {
	calls       []string        // every called name, bare or selector, as journalscan records them
	callees     map[string]bool // keys of the package functions the body calls or takes as a value
	ownImageDML bool            // the body's own SQL writes a bead image table (sqlWritesImageTable)
}

func (f *doltFunc) callsAnyOf(set map[string]bool) bool {
	for _, c := range f.calls {
		if set[c] {
			return true
		}
	}
	return false
}

// parseDoltCallGraph type-checks this package's non-test files, plus any
// fixture sources, and resolves each reference to a package function by type:
// m.store.withRetryTxNoJournal is DoltStore's method and rows.Close() is
// *sql.Rows', not issueOperations.Close. Standard-library imports are checked
// from GOROOT source; every other import is refused, and go/types gives it an
// empty package and carries on, so every type this package declares and every
// stdlib type still resolves.
//
// The stdlib must resolve: without it rows.Close() has no known type and
// becomes an edge to every package Close, so the plumbing reads as journaling
// and every NoJournal caller is flagged. A sandbox may hold the Go SDK without
// its sources (Bazel declares this package's sources, not GOROOT's). There the
// guard skips, naming the missing SDK, rather than report a call graph with
// holes. The compiler's export data is no fallback: the SDK ships no
// precompiled standard library, and importer.Default builds it with
// `go list -export`, which needs the same GOROOT sources.
//
// Where the type is not known, the edge is kept, by bare name, to every
// package method of that name:
//   - a selector on a value of another module's type (issueops.BlockedRecheck,
//     ...), whose type the refused import left unknown;
//   - an interface method, which may dispatch to any package type.
//
// Not followed at all: calls through a func-typed value (a variable, field or
// parameter), whose target is decided at run time. A NoJournal body's own
// closure is part of its caller's body, so it is checked; a method or function
// passed as a value is an edge like a call.
func parseDoltCallGraph(t *testing.T, fixtures ...string) doltCallGraph {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}
	var files []*ast.File
	for _, file := range pkgs["dolt"].Files {
		files = append(files, file)
	}
	for i, src := range fixtures {
		file, err := parser.ParseFile(fset, fmt.Sprintf("fixture%d.go", i), src, 0)
		if err != nil {
			t.Fatalf("parse fixture %d: %v", i, err)
		}
		files = append(files, file)
	}
	info := &gotypes.Info{
		Uses:       map[*ast.Ident]gotypes.Object{},
		Selections: map[*ast.SelectorExpr]*gotypes.Selection{},
	}
	imp := &stdlibImporter{src: importer.ForCompiler(fset, "source", nil), missing: map[string]error{}}
	conf := gotypes.Config{
		Importer: imp,
		Error:    func(error) {}, // the refused imports; resolution goes on without them
	}
	pkg, _ := conf.Check("dolt", fset, files, info)
	if len(imp.missing) > 0 {
		paths := make([]string, 0, len(imp.missing))
		for path := range imp.missing {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		t.Skipf("the Go SDK's standard library sources are unavailable to the type checker (GOROOT %q does not load %v; first error: %v), so the call graph cannot be built without false journaling edges",
			build.Default.GOROOT, paths, imp.missing[paths[0]])
	}

	g := doltCallGraph{}
	byName := map[string][]string{} // method name -> package method keys
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := funcDeclKey(fn)
			if fn.Recv != nil {
				byName[fn.Name.Name] = append(byName[fn.Name.Name], key)
			}
			g[key] = &doltFunc{callees: map[string]bool{}}
		}
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			f := g[funcDeclKey(fn)]
			f.ownImageDML = bodyWritesImageTable(fn.Body)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					switch fun := node.Fun.(type) {
					case *ast.Ident:
						f.calls = append(f.calls, fun.Name)
					case *ast.SelectorExpr:
						f.calls = append(f.calls, fun.Sel.Name)
					}
				case *ast.SelectorExpr:
					if info.Uses[node.Sel] != nil || info.Selections[node] != nil {
						return true
					}
					if x, ok := node.X.(*ast.Ident); ok {
						if _, ok := info.Uses[x].(*gotypes.PkgName); ok {
							return true // a qualified identifier of another package
						}
					}
					for _, callee := range byName[node.Sel.Name] {
						f.callees[callee] = true
					}
				case *ast.Ident:
					target, ok := info.Uses[node].(*gotypes.Func)
					if !ok {
						return true
					}
					sig := target.Type().(*gotypes.Signature)
					if sig.Recv() == nil {
						if target.Pkg() == pkg {
							f.callees[target.Name()] = true
						}
						return true
					}
					recv := sig.Recv().Type()
					if ptr, ok := recv.(*gotypes.Pointer); ok {
						recv = ptr.Elem()
					}
					if gotypes.IsInterface(recv) {
						for _, callee := range byName[target.Name()] {
							f.callees[callee] = true
						}
						return true
					}
					if named, ok := recv.(*gotypes.Named); ok && target.Pkg() == pkg {
						f.callees[named.Obj().Name()+"."+target.Name()] = true
					}
				}
				return true
			})
		}
	}
	return g
}

// funcDeclKey keys fn the way journalscan.ParsePackage does.
func funcDeclKey(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return journalscan.ReceiverTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// stdlibImporter checks standard-library imports from source and refuses
// every other path (see parseDoltCallGraph).
type stdlibImporter struct {
	src gotypes.Importer
	// missing records each standard-library package src failed to load; a
	// refused non-stdlib import is expected and not recorded.
	missing map[string]error
}

func (i *stdlibImporter) Import(path string) (*gotypes.Package, error) {
	if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
		return nil, fmt.Errorf("%s: not a standard-library package", path)
	}
	pkg, err := i.src.Import(path)
	if err != nil {
		i.missing[path] = err
	}
	return pkg, err
}

// TestJournalWriterLock_NonJournaledWritesSkipAHeldLock pins the audit end to
// end: while another session holds the journal writer lock, writes that touch
// no bead image table complete inside a deadline far shorter than one GET_LOCK
// wait, and a bead write in the same window still waits — events journal on or
// off.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestJournalWriterLock_NonJournaledWritesSkipAHeldLock(t *testing.T) {
	primary, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	holder := openSecondStoreOnBranch(t, ctx, primary)
	conn, err := holder.db.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer conn.Close()
	lockName := issueops.JournalWriterLockName(primary.database)
	// One writer store per journal state, all opened while primary's session
	// is still on the test branch: a write cut off in its GET_LOCK wait drops
	// its store's single session, and the replacement starts on the default
	// branch.
	writers := map[bool]*DoltStore{true: openSecondStoreOnBranch(t, ctx, primary), false: openSecondStoreOnBranch(t, ctx, primary)}

	for _, journal := range []bool{true, false} {
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			store := writers[journal]
			store.SetEventsJournalEnabled(journal)
			mems, err := store.Memories()
			if err != nil {
				t.Fatalf("memories: %v", err)
			}
			id := fmt.Sprintf("bd-nj-%v", journal)
			if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: "nj", IssueType: types.TypeTask, Status: types.StatusOpen}, "actor"); err != nil {
				t.Fatalf("create: %v", err)
			}

			var got int
			if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", lockName).Scan(&got); err != nil || got != 1 {
				t.Fatalf("hold journal writer lock: got=%d err=%v", got, err)
			}
			defer func() { _, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName) }()

			const deadline = time.Second
			for _, tc := range []struct {
				name string
				call func(context.Context) error
			}{
				{"SetRepoMtime", func(ctx context.Context) error {
					return store.SetRepoMtime(ctx, "/tmp/bd-nj/repo", "/tmp/bd-nj/repo/issues.jsonl", 1)
				}},
				{"ClearRepoMtime", func(ctx context.Context) error { return store.ClearRepoMtime(ctx, "/tmp/bd-nj/repo") }},
				{"Remember", func(ctx context.Context) error {
					_, err := mems.Remember(ctx, memoryops.RememberRequest{Key: "nj", Content: "held"})
					return err
				}},
				{"Forget", func(ctx context.Context) error {
					_, err := mems.Forget(ctx, memoryops.ForgetRequest{Key: "nj"})
					return err
				}},
			} {
				callCtx, cancel := context.WithTimeout(ctx, deadline)
				start := time.Now()
				err := tc.call(callCtx)
				cancel()
				t.Logf("%s under a held journal lock: %s", tc.name, time.Since(start))
				if err != nil {
					t.Errorf("%s, which writes no bead image table, waited on the held journal writer lock: %v", tc.name, err)
				}
			}

			callCtx, cancel := context.WithTimeout(ctx, deadline)
			defer cancel()
			start := time.Now()
			err = store.UpdateIssue(callCtx, id, map[string]interface{}{"title": "held"}, "actor")
			t.Logf("UpdateIssue under a held journal lock: %s (err=%v)", time.Since(start), err)
			if err == nil {
				t.Fatal("UpdateIssue committed while another session held the journal writer lock; a bead write must wait")
			}
		})
	}
}

// TestJournalWriterLock_HeartbeatWaitsOnAHeldLock pins that a lease heartbeat
// queues on the journal writer lock, events journal on or off: a re-arm records
// a lease_rearmed event and a journal entry (RenewLeaseInTx), so the heartbeat
// runs on the locked route, and under a held lock it fails its short deadline
// in the lock wait. Once the lock is free it lands.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestJournalWriterLock_HeartbeatWaitsOnAHeldLock(t *testing.T) {
	primary, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	holder := openSecondStoreOnBranch(t, ctx, primary)
	conn, err := holder.db.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer conn.Close()
	lockName := issueops.JournalWriterLockName(primary.database)
	// Per journal state, one store for the heartbeat cut off in its GET_LOCK
	// wait and one for the free-lock pass, all opened while primary's session
	// is still on the test branch: the cut-off wait drops its store's single
	// session, and the replacement starts on the default branch.
	type heartbeaters struct{ held, free *DoltStore }
	stores := map[bool]heartbeaters{}
	for _, journal := range []bool{true, false} {
		stores[journal] = heartbeaters{held: openSecondStoreOnBranch(t, ctx, primary), free: openSecondStoreOnBranch(t, ctx, primary)}
	}

	for _, journal := range []bool{true, false} {
		t.Run(fmt.Sprintf("journal=%v", journal), func(t *testing.T) {
			held, free := stores[journal].held, stores[journal].free
			held.SetEventsJournalEnabled(journal)
			free.SetEventsJournalEnabled(journal)
			id := fmt.Sprintf("bd-hbw-%v", journal)
			if err := free.CreateIssue(ctx, &types.Issue{ID: id, Title: "hbw", IssueType: types.TypeTask, Status: types.StatusOpen}, "actor"); err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := free.ClaimIssue(ctx, id, "worker"); err != nil {
				t.Fatalf("claim: %v", err)
			}

			var got int
			if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", lockName).Scan(&got); err != nil || got != 1 {
				t.Fatalf("hold journal writer lock: got=%d err=%v", got, err)
			}
			released := false
			defer func() {
				if !released {
					_, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName)
				}
			}()

			const deadline = time.Second
			callCtx, cancel := context.WithTimeout(ctx, deadline)
			start := time.Now()
			err := held.HeartbeatIssue(callCtx, id, "worker")
			waited := time.Since(start)
			cancel()
			t.Logf("HeartbeatIssue under a held journal lock: %s (err=%v)", waited, err)
			if err == nil {
				t.Fatal("HeartbeatIssue committed while another session held the journal writer lock; a heartbeat may re-arm, which journals, so it must wait")
			}
			if !strings.Contains(err.Error(), "journal writer lock") {
				t.Errorf("HeartbeatIssue failed outside the journal writer lock wait: %v", err)
			}

			if _, err := conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName); err != nil {
				t.Fatalf("release journal writer lock: %v", err)
			}
			released = true
			if err := free.HeartbeatIssue(ctx, id, "worker"); err != nil {
				t.Errorf("HeartbeatIssue with the lock free: %v", err)
			}
		})
	}
}

// TestJournalWriterLock_CompactionWaitsOnAHeldLock pins that the compaction
// writes, which record no journal event but write the issues row (or archive
// it), take the journal writer lock with the journal off: under a held lock
// each one fails its short deadline, and once the lock is free each one lands.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestJournalWriterLock_CompactionWaitsOnAHeldLock(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	// The free-lock pass runs on its own store, opened while store's session
	// is on the test branch: a write cut off in its GET_LOCK wait drops the
	// single session, and the replacement starts on the default branch.
	free := openSecondStoreOnBranch(t, ctx, store)
	const id = "bd-cw-1"
	if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: "cw", Description: "long text", IssueType: types.TypeTask, Status: types.StatusClosed}, "actor"); err != nil {
		t.Fatalf("create: %v", err)
	}

	holder := openSecondStoreOnBranch(t, ctx, store)
	conn, err := holder.db.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer conn.Close()
	lockName := issueops.JournalWriterLockName(store.database)

	writes := []struct {
		name string
		call func(context.Context, *DoltStore) error
	}{
		{"SnapshotIssue", func(ctx context.Context, s *DoltStore) error { return s.SnapshotIssue(ctx, id, 1) }},
		{"ApplyCompaction", func(ctx context.Context, s *DoltStore) error { return s.ApplyCompaction(ctx, id, 1, 9, 0, "") }},
		{"RestoreFromSnapshot", func(ctx context.Context, s *DoltStore) error {
			_, err := s.RestoreFromSnapshot(ctx, id)
			return err
		}},
	}

	var got int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", lockName).Scan(&got); err != nil || got != 1 {
		t.Fatalf("hold journal writer lock: got=%d err=%v", got, err)
	}
	for _, w := range writes {
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		start := time.Now()
		err := w.call(callCtx, store)
		cancel()
		t.Logf("%s under a held journal lock: %s (err=%v)", w.name, time.Since(start), err)
		if err == nil {
			t.Errorf("%s committed while another session held the journal writer lock; a compaction write must wait", w.name)
		}
	}
	if _, err := conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName); err != nil {
		t.Fatalf("release journal writer lock: %v", err)
	}
	for _, w := range writes {
		if err := w.call(ctx, free); err != nil {
			t.Errorf("%s with the lock free: %v", w.name, err)
		}
	}
}

// openSecondStoreOnBranch opens another store on primary's database with its
// own single-connection pool, checked out to primary's test branch. Two
// writers on two pools share nothing client-side, so only the server's named
// lock can serialize them.
func openSecondStoreOnBranch(t *testing.T, ctx context.Context, primary *DoltStore) *DoltStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "dolt-test-second-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	second, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       primary.database,
		MaxOpenConns:   1, // DOLT_CHECKOUT below is session-level
	})
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	var branch string
	if err := primary.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		t.Fatalf("read test branch: %v", err)
	}
	if _, err := second.db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
		t.Fatalf("check second store out to %s: %v", branch, err)
	}
	return second
}

// openRecordingStoreOnBranch is openSecondStoreOnBranch with the store's pool
// swapped for a single-session one behind a recordingConnector: its sessions
// log every statement in rec and, with a gate, hold their journaled commits
// there.
func openRecordingStoreOnBranch(t *testing.T, ctx context.Context, primary *DoltStore, rec *sqlRecorder, gate *commitGate) *DoltStore {
	t.Helper()
	store := openSecondStoreOnBranch(t, ctx, primary)
	db := sql.OpenDB(recordingConnector{dsn: store.connStr, drv: store.db.Driver(), rec: rec, gate: gate})
	db.SetMaxOpenConns(1) // DOLT_CHECKOUT below is session-level
	_ = store.db.Close()
	store.db = db // closed with the store
	var branch string
	if err := primary.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		t.Fatalf("read test branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
		t.Fatalf("check recording store out to %s: %v", branch, err)
	}
	return store
}

// setupSerialTestStore is setupTestStore without t.Parallel, for the live
// tests that hold or wait on the journal writer lock. The lock's name carries
// the database, so on the shared test database every test's journaled writer
// queues on it, and another test's holder can make a writer wait that should
// not, or take the lock a probe expects free. Go starts the parallel tests only
// after every sequential one has finished, so a test on this store has the
// lock to itself.
func setupSerialTestStore(t *testing.T) (*DoltStore, func()) {
	t.Helper()
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	if testSharedDB == "" {
		t.Fatal("testSharedDB not set — TestMain did not initialize shared database")
	}
	ctx, cancel := testContext(t)
	defer cancel()
	tmpDir, err := os.MkdirTemp("", "dolt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	store, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       testSharedDB,
		MaxOpenConns:   1, // DOLT_CHECKOUT is session-level
	})
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to create Dolt store: %v", err)
	}
	_, branchCleanup := testutil.StartTestBranch(t, store.db, testSharedDB)
	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
		branchCleanup()
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to initialize branch-local ignored schema: %v", err)
	}
	return store, func() {
		branchCleanup()
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
}

// tryJournalLock reports whether conn's session could take the journal writer
// lock without waiting, and gives it straight back if it could.
func tryJournalLock(t *testing.T, ctx context.Context, conn *sql.Conn, name string) bool {
	t.Helper()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&got); err != nil {
		t.Fatalf("try journal writer lock: %v", err)
	}
	if got.Valid && got.Int64 == 1 {
		if _, err := conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", name); err != nil {
			t.Fatalf("give back journal writer lock: %v", err)
		}
		return true
	}
	return false
}

// TestEventsJournal_OverlappingServerWritersQueue pins the journal writer
// lock (#6117). The long writer and the short one run on different stores
// with their own pools on the same database and branch, so nothing but the
// server-side GET_LOCK can make the short writer wait.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestEventsJournal_OverlappingServerWritersQueue(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	second := openSecondStoreOnBranch(t, ctx, store)
	assertJournaledWritersQueue(t, ctx, store, second)
}

// TestEventsJournalOff_OverlappingServerWritersQueue is
// TestEventsJournal_OverlappingServerWritersQueue with the journal off: the
// long writer and the short one write different tables of one bead, so
// without the lock nothing would make the short one wait and it would
// land inside the long one's read-then-write window. With the lock it queues
// and commits after the long one.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestEventsJournalOff_OverlappingServerWritersQueue(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	second := openSecondStoreOnBranch(t, ctx, store)
	const id = "bd-qoff-1"
	createLockOrderIssues(t, ctx, store, id)

	attempts := 0
	inside := make(chan struct{})
	release := make(chan struct{})
	releaseLong := sync.OnceFunc(func() { close(release) })
	defer releaseLong()
	longDone := make(chan error, 1)
	go func() {
		longDone <- store.withRetryTx(ctx, func(tx *sql.Tx) error {
			attempts++
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET title = 'long' WHERE id = ?", id); err != nil {
				return err
			}
			if attempts == 1 {
				close(inside)
				<-release
			}
			return nil
		})
	}()
	<-inside

	shortDone := make(chan error, 1)
	go func() { shortDone <- second.AddLabel(ctx, id, "queued", "actor") }()
	select {
	case err := <-shortDone:
		t.Fatalf("short bead write finished (err=%v) while the long one held the lock with the journal off; it must queue", err)
	case <-time.After(500 * time.Millisecond):
	}
	releaseLong()
	if err := <-longDone; err != nil {
		t.Fatalf("long tx: %v", err)
	}
	if err := <-shortDone; err != nil {
		t.Fatalf("short write: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("long body ran %d times, want 1", attempts)
	}
	labels, err := store.GetLabels(ctx, id)
	if err != nil {
		t.Fatalf("labels: %v", err)
	}
	if !slices.Contains(labels, "queued") {
		t.Fatalf("labels = %v, want the queued writer's label after the long write", labels)
	}
}

// assertJournaledWritersQueue runs a long journaled transaction on long and,
// inside its window, a short journaled update on short; both stores must be on
// one database and branch. Two journaled transactions always write the same
// bd_events_seq cell, so without the lock the short writer commits inside the
// long one's window and the long one loses with 1213 and replays its whole
// body. With the lock the short writer waits, the long body runs once, and the
// seqs stay contiguous in commit order.
func assertJournaledWritersQueue(t *testing.T, ctx context.Context, long, short *DoltStore) {
	t.Helper()
	enableJournalForTest(t, long)
	enableJournalForTest(t, short)
	for _, id := range []string{"bd-q-1", "bd-q-2"} {
		if err := long.CreateIssue(ctx, &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen}, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	clearJournal(t, long)

	attempts := 0
	inside := make(chan struct{})
	release := make(chan struct{})
	// Let the long body finish however this ends, so a failure below does not
	// strand it holding the lock while cleanup closes the stores.
	releaseLong := sync.OnceFunc(func() { close(release) })
	defer releaseLong()
	longDone := make(chan error, 1)
	go func() {
		longDone <- long.withRetryTx(ctx, func(tx *sql.Tx) error {
			attempts++
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET title = 'long' WHERE id = 'bd-q-1'"); err != nil {
				return err
			}
			if err := issueops.RecordEventInTx(ctx, tx, issueops.EventUpdate, "bd-q-1", "actor"); err != nil {
				return err
			}
			if attempts == 1 {
				close(inside)
				<-release
			}
			return nil
		})
	}()
	<-inside

	shortDone := make(chan error, 1)
	go func() {
		shortDone <- short.UpdateIssue(ctx, "bd-q-2", map[string]interface{}{"title": "short"}, "actor")
	}()
	select {
	case err := <-shortDone:
		t.Fatalf("short journaled write finished (err=%v) while the long one held the journal; it must queue", err)
	case <-time.After(500 * time.Millisecond):
	}
	releaseLong()
	if err := <-longDone; err != nil {
		t.Fatalf("long tx: %v", err)
	}
	if err := <-shortDone; err != nil {
		t.Fatalf("short write: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("long body ran %d times, want 1: a replay means it lost a 1213 to the short writer", attempts)
	}
	rows := readJournalRows(t, long)
	if len(rows) < 2 || rows[0].id != "bd-q-1" {
		t.Fatalf("journal rows = %+v, want the long write's row first", rows)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].seq != rows[i-1].seq+1 {
			t.Fatalf("journal seqs not contiguous: %+v", rows)
		}
	}
}

// createLockOrderIssues creates one open task per id on store.
func createLockOrderIssues(t *testing.T, ctx context.Context, store *DoltStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen}, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
}

// parkJournaledHolder starts a journaled withRetryTx body on holder that
// retitles issue id to title and records the event, and returns once the body
// is parked inside its transaction, holding the journal writer lock. The body
// commits when release runs; done carries the result. Callers defer release
// so a failure does not strand the holder while the stores close.
func parkJournaledHolder(t *testing.T, ctx context.Context, holder *DoltStore, id, title string) (release func(), done <-chan error) {
	t.Helper()
	inside := make(chan struct{})
	gate := make(chan struct{})
	release = sync.OnceFunc(func() { close(gate) })
	result := make(chan error, 1)
	parked := false
	go func() {
		result <- holder.withRetryTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", title, id); err != nil {
				return err
			}
			if err := issueops.RecordEventInTx(ctx, tx, issueops.EventUpdate, id, "actor"); err != nil {
				return err
			}
			if !parked {
				parked = true
				close(inside)
				<-gate
			}
			return nil
		})
	}()
	select {
	case <-inside:
	case err := <-result:
		t.Fatalf("journaled holder finished (err=%v) before parking inside its transaction", err)
	}
	return release, result
}

// assertJournalFollowsHolder checks the journal a parked holder and a writer
// queued behind it left: the holder's row first, then at least one of the
// writer's, the seqs contiguous. A writer whose transaction began before the
// holder committed would have read bd_events_seq stale and lost its commit.
func assertJournalFollowsHolder(t *testing.T, store *DoltStore, holderID string) {
	t.Helper()
	rows := readJournalRows(t, store)
	if len(rows) < 2 || rows[0].id != holderID {
		t.Fatalf("journal rows = %+v, want the holder's %s row first and the queued writer's after it", rows, holderID)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].seq != rows[i-1].seq+1 {
			t.Fatalf("journal seqs not contiguous: %+v", rows)
		}
	}
}

// TestJournalWriterLock_RecordingRouteHoldsAcrossCommit pins that
// runDoltTransactionRecording, the transaction behind RunInTransaction, keeps
// the journal writer lock until it has committed. Its body does a journaled
// write and parks; a journaled write on another store, which only the server
// lock can hold back, must still be waiting half a second later. Released any
// earlier — after BEGIN, say — the short writer commits inside the window and
// the long one loses its commit with 1213.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestJournalWriterLock_RecordingRouteHoldsAcrossCommit(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	second := openSecondStoreOnBranch(t, ctx, store)
	enableJournalForTest(t, store)
	enableJournalForTest(t, second)
	createLockOrderIssues(t, ctx, store, "bd-rh-1", "bd-rh-2")

	calls := 0
	inside := make(chan struct{})
	release := make(chan struct{})
	releaseLong := sync.OnceFunc(func() { close(release) })
	defer releaseLong()
	longDone := make(chan error, 1)
	go func() {
		longDone <- store.RunInTransaction(ctx, "bd: recording route holds the journal lock", func(tx storage.Transaction) error {
			calls++
			if err := tx.UpdateIssue(ctx, "bd-rh-1", map[string]interface{}{"title": "long"}, "actor"); err != nil {
				return err
			}
			if calls == 1 {
				close(inside)
				<-release
			}
			return nil
		})
	}()
	select {
	case <-inside:
	case err := <-longDone:
		t.Fatalf("recording transaction finished (err=%v) before its body parked", err)
	}

	shortDone := make(chan error, 1)
	go func() {
		shortDone <- second.UpdateIssue(ctx, "bd-rh-2", map[string]interface{}{"title": "short"}, "actor")
	}()
	select {
	case err := <-shortDone:
		t.Fatalf("short journaled write finished (err=%v) while a recording transaction held the journal; it must queue until that commits", err)
	case <-time.After(500 * time.Millisecond):
	}
	releaseLong()
	if err := <-longDone; err != nil {
		t.Fatalf("recording transaction: %v", err)
	}
	if err := <-shortDone; err != nil {
		t.Fatalf("short write: %v", err)
	}
	if calls != 1 {
		t.Fatalf("recording body ran %d times, want 1", calls)
	}
}

// TestJournalWriterLock_RecordingRouteLocksBeforeBegin pins that
// runDoltTransactionRecording takes the journal writer lock before it begins
// its transaction. Another store's journaled writer holds the lock first, with
// an uncommitted retitle; the recording transaction, started meanwhile, reads
// that issue and does a journaled write of its own. Locked before BEGIN, its
// snapshot follows the holder's commit: it reads the new title and commits.
// Locked after BEGIN, its snapshot predates that commit, so it reads the old
// title and its bd_events_seq write fails with 1213.
//
// It runs alone (setupSerialTestStore), so no other test's writer can hold
// the lock it queues on.
func TestJournalWriterLock_RecordingRouteLocksBeforeBegin(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	second := openSecondStoreOnBranch(t, ctx, store)
	enableJournalForTest(t, store)
	enableJournalForTest(t, second)
	createLockOrderIssues(t, ctx, store, "bd-rb-1", "bd-rb-2")
	clearJournal(t, store)

	releaseHolder, holderDone := parkJournaledHolder(t, ctx, second, "bd-rb-1", "held")
	defer releaseHolder()

	calls := 0
	var seen string
	done := make(chan error, 1)
	go func() {
		done <- store.RunInTransaction(ctx, "bd: recording route locks before begin", func(tx storage.Transaction) error {
			calls++
			issue, err := tx.GetIssue(ctx, "bd-rb-1")
			if err != nil {
				return err
			}
			seen = issue.Title
			return tx.UpdateIssue(ctx, "bd-rb-2", map[string]interface{}{"title": "after " + issue.Title}, "actor")
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("recording transaction finished (err=%v) while another journaled writer held the lock; it must queue", err)
	case <-time.After(500 * time.Millisecond):
	}
	releaseHolder()
	if err := <-holderDone; err != nil {
		t.Fatalf("holder: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("recording transaction behind a committed holder: %v (a 1213 means it began before the holder committed)", err)
	}
	if calls != 1 {
		t.Fatalf("recording body ran %d times, want 1", calls)
	}
	if seen != "held" {
		t.Fatalf("recording body read title %q, want the holder's committed %q: its snapshot predates the holder's commit", seen, "held")
	}
	assertJournalFollowsHolder(t, store, "bd-rb-1")
}

// TestJournalWriterLock_RawSitesQueueAndBeginAfterTheHolder pins the lock
// order on the write paths that pin a connection with writeTxConn and run a
// bare BeginTx/Commit on it, with no retry: wisp updates, closes and slot
// writes, dependency removals and comments. Each site's public entry runs on a
// store whose session holds its journaled commit at a gate, and starts while
// another store's journaled writer is parked holding the lock. It must still
// be waiting half a second later (it queues). Once the holder commits, the
// entry reaches its commit holding the lock: another session cannot take it
// until the commit has gone through, and then can (it keeps the lock to its
// commit). It must succeed with its journal rows after the holder's (it began
// after the holder committed: a transaction begun earlier reads bd_events_seq
// stale and fails with 1213).
//
// It runs alone (setupSerialTestStore), so a probe that finds the lock taken
// cannot be reading another test's writer.
func TestJournalWriterLock_RawSitesQueueAndBeginAfterTheHolder(t *testing.T) {
	store, cleanup := setupSerialTestStore(t)
	defer cleanup()
	ctx := context.Background()
	second := openSecondStoreOnBranch(t, ctx, store)
	gate := &commitGate{}
	raw := openRecordingStoreOnBranch(t, ctx, store, &sqlRecorder{}, gate)
	enableJournalForTest(t, store)
	enableJournalForTest(t, second)
	enableJournalForTest(t, raw)
	const holderID, blockerID = "bd-rs-holder", "bd-rs-blocker"
	createLockOrderIssues(t, ctx, store, holderID, blockerID)
	lockName := issueops.JournalWriterLockName(store.database)
	const bound = 10 * time.Second

	addBlocker := func(ctx context.Context, s *DoltStore, id string) error {
		return s.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: blockerID, Type: types.DepBlocks}, "actor")
	}
	removeBlocker := func(ctx context.Context, s *DoltStore, id string) error {
		return s.RemoveDependencyWithOptions(ctx, id, blockerID, "actor", storage.DependencyRemoveOptions{EmitEvent: true})
	}
	for i, site := range []struct {
		name  string
		wisp  bool
		prep  func(ctx context.Context, s *DoltStore, id string) error
		write func(ctx context.Context, s *DoltStore, id string) error
	}{
		{name: "updateWisp", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.UpdateIssue(ctx, id, map[string]interface{}{"title": "raw"}, "actor")
		}},
		{name: "closeWisp", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.CloseIssue(ctx, id, "done", "actor", "")
		}},
		{name: "mergeMetadataWisp", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.SlotSet(ctx, id, "slot", "raw", "actor")
		}},
		{name: "clearMetadataWisp", wisp: true,
			prep: func(ctx context.Context, s *DoltStore, id string) error {
				return s.SlotSet(ctx, id, "slot", "raw", "actor")
			},
			write: func(ctx context.Context, s *DoltStore, id string) error {
				return s.SlotClear(ctx, id, "slot", "actor")
			}},
		{name: "updateWispChecked", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.UpdateIssueChecked(ctx, id, map[string]interface{}{"title": "raw"}, "actor", storage.UpdateIssueOptions{})
		}},
		{name: "closeWispChecked", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			_, err := s.CloseIssueChecked(ctx, id, "actor", storage.CloseIssueOptions{Reason: "done"})
			return err
		}},
		{name: "deleteWisp", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.DeleteIssue(ctx, id)
		}},
		{name: "deleteWispBatchTx", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			_, err := s.DeleteIssues(ctx, []string{id}, false, false, false)
			return err
		}},
		{name: "claimWisp", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.ClaimIssue(ctx, id, "actor")
		}},
		{name: "addWispDependency", wisp: true, write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.AddDependencyWithOptions(ctx, &types.Dependency{IssueID: id, DependsOnID: blockerID, Type: types.DepBlocks}, "actor", storage.DependencyAddOptions{EmitEvent: true})
		}},
		{name: "RemoveDependencyWithOptions wisp", wisp: true, prep: addBlocker, write: removeBlocker},
		{name: "RemoveDependencyWithOptions issue", prep: addBlocker, write: removeBlocker},
		{name: "AddComment", write: func(ctx context.Context, s *DoltStore, id string) error {
			return s.AddComment(ctx, id, "actor", "raw")
		}},
	} {
		t.Run(site.name, func(t *testing.T) {
			id := fmt.Sprintf("bd-rs-%d", i)
			if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: site.wisp}, "actor"); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
			if site.prep != nil {
				if err := site.prep(ctx, store, id); err != nil {
					t.Fatalf("prepare %s: %v", id, err)
				}
			}
			clearJournal(t, store)

			held, resume := gate.arm()
			releaseHolder, holderDone := parkJournaledHolder(t, ctx, second, holderID, "held by "+site.name)
			var writeErr error
			written := make(chan struct{})
			// However this ends, let the holder and the gated commit go and
			// wait, bounded, for the entry before the stores close.
			defer func() {
				releaseHolder()
				resume()
				select {
				case <-written:
				case <-time.After(bound):
				}
			}()
			go func() {
				defer close(written)
				writeErr = site.write(ctx, raw, id)
			}()
			select {
			case <-written:
				t.Fatalf("%s finished (err=%v) while another journaled writer held the lock; it must queue", site.name, writeErr)
			case <-held:
				t.Fatalf("%s reached its journaled commit while another journaled writer held the lock; it must queue", site.name)
			case <-time.After(500 * time.Millisecond):
			}
			releaseHolder()
			if err := <-holderDone; err != nil {
				t.Fatalf("holder: %v", err)
			}

			select {
			case <-held:
			case <-written:
				t.Fatalf("%s finished (err=%v) without a journaled commit reaching the gate", site.name, writeErr)
			case <-time.After(bound):
				t.Fatalf("%s did not reach its journaled commit within %s of the holder committing", site.name, bound)
			}
			probe, err := second.db.Conn(ctx)
			if err != nil {
				t.Fatalf("probe conn: %v", err)
			}
			defer probe.Close()
			if tryJournalLock(t, ctx, probe, lockName) {
				t.Errorf("another session took the journal writer lock while %s sat between its journaled writes and its commit; it must keep the lock until it has committed", site.name)
			}
			resume()
			select {
			case <-written:
			case <-time.After(bound):
				t.Fatalf("%s did not finish within %s of its commit going ahead", site.name, bound)
			}
			if writeErr != nil {
				t.Fatalf("%s behind a committed holder: %v (a 1213 means it began before the holder committed)", site.name, writeErr)
			}
			for deadline := time.Now().Add(bound); !tryJournalLock(t, ctx, probe, lockName); time.Sleep(50 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("the journal writer lock was still held %s after %s committed", bound, site.name)
				}
			}
			assertJournalFollowsHolder(t, store, holderID)
		})
	}
}
