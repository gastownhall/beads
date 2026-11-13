package dolt

import (
	"context"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// The defer-wake sweep runs before every ready read and claim, and almost
// always finds nothing due. These tests pin what that costs a server-mode store,
// counted at the driver, and what the one-statement preflight that now decides
// whether to open the sweep's write transaction does in each case it can meet.
// sqlmock expects statements in order and refuses any it was not told about, so
// each script below is the whole conversation.

// deferWakePreflight matches the preflight over both tables.
const deferWakePreflight = `SELECT EXISTS \(SELECT 1 FROM issues WHERE status = 'deferred' AND defer_until IS NOT NULL AND defer_until <= UTC_TIMESTAMP\(\)\), ` +
	`EXISTS \(SELECT 1 FROM wisps WHERE status = 'deferred'`

// deferWakePreflightIssuesOnly matches the form used when wisps is missing.
const deferWakePreflightIssuesOnly = `^SELECT EXISTS \(SELECT 1 FROM issues WHERE status = 'deferred' AND defer_until IS NOT NULL AND defer_until <= UTC_TIMESTAMP\(\)\)$`

// deferWakeRoundTrips counts every statement sent, START TRANSACTION, COMMIT
// and ROLLBACK included; sqlcount's Total leaves those three out.
func deferWakeRoundTrips(s sqlcount.Snapshot) int64 {
	return s.Total() + s.Begin + s.Commit + s.Rollback
}

func expectDeferWakePreflight(mock sqlmock.Sqlmock, issuesDue, wispsDue int) {
	mock.ExpectQuery(deferWakePreflight).
		WillReturnRows(sqlmock.NewRows([]string{"issues", "wisps"}).AddRow(issuesDue, wispsDue))
}

// expectWispWake scripts the write transaction of a sweep that wakes one wisp.
// A wisp keeps the script short: its wake mints no Dolt commit.
func expectWispWake(mock sqlmock.Sqlmock, id string) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM issues WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT id FROM wisps WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	mock.ExpectExec(`UPDATE wisps SET status = 'open', defer_until = NULL`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), id).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM wisp_events WHERE issue_id`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`INSERT INTO wisp_events`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// TestDeferWakeSweepWithNothingDueSendsOneStatement: a sweep with nothing due
// sends the preflight and nothing else. It used to send START TRANSACTION, the
// snapshot SELECT for issues and for wisps, and COMMIT: four statements.
func TestDeferWakeSweepWithNothingDueSendsOneStatement(t *testing.T) {
	store, mock, counts := vhCountedStore(t)
	expectAutocommit(mock, 1)
	expectDeferWakePreflight(mock, 0, 0)
	expectDeferWakePreflight(mock, 0, 0)

	store.wakeExpiredDefers(context.Background())
	first := counts.Snapshot()
	store.wakeExpiredDefers(context.Background())
	second := counts.Snapshot().Sub(first)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := counts.Snapshot(); got.Begin != 0 || got.Commit != 0 || got.Rollback != 0 {
		t.Errorf("a sweep with nothing due opened a transaction: %+v", got)
	}
	if n := deferWakeRoundTrips(first); n != 2 {
		t.Errorf("first sweep sent %d statements, want the store's one autocommit read and the preflight: %+v", n, first)
	}
	if n := deferWakeRoundTrips(second); n != 1 {
		t.Errorf("second sweep sent %d statements, want the preflight alone: %+v", n, second)
	}
}

// TestDeferWakeSweepWithSomethingDueOpensTheWrite: a due answer opens the same
// write transaction the sweep always ran, so a due sweep costs one statement
// more than it did.
func TestDeferWakeSweepWithSomethingDueOpensTheWrite(t *testing.T) {
	store, mock, counts := vhCountedStore(t)
	expectAutocommit(mock, 1)
	expectDeferWakePreflight(mock, 0, 1)
	expectWispWake(mock, "bd-wisp-1")

	store.wakeExpiredDefers(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	got := counts.Snapshot()
	if got.Begin != 1 || got.Commit != 1 || got.Rollback != 0 {
		t.Errorf("a due sweep ran %+v, want one committed write transaction", got)
	}
	// Autocommit read, preflight, then the write: BEGIN, two snapshot
	// SELECTs, UPDATE, event SELECT and INSERT, COMMIT.
	if n := deferWakeRoundTrips(got); n != 9 {
		t.Errorf("a due sweep sent %d statements, want 9: %+v", n, got)
	}
}

// TestDeferWakePreflightWithoutWispsTable: a database without the wisps table
// gets the issues-only preflight, as the sweep itself tolerates the missing
// table, and nothing due still opens no transaction.
func TestDeferWakePreflightWithoutWispsTable(t *testing.T) {
	store, mock, counts := vhCountedStore(t)
	expectAutocommit(mock, 1)
	mock.ExpectQuery(deferWakePreflight).
		WillReturnError(&mysql.MySQLError{Number: 1146, Message: "table not found: wisps"})
	mock.ExpectQuery(deferWakePreflightIssuesOnly).
		WillReturnRows(sqlmock.NewRows([]string{"issues"}).AddRow(0))

	stderr := captureOpenStderr(t, func() { store.wakeExpiredDefers(context.Background()) })

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := counts.Snapshot(); got.Begin != 0 {
		t.Errorf("nothing due opened a transaction: %+v", got)
	}
	if stderr != "" {
		t.Errorf("a missing wisps table warned: %q", stderr)
	}
}

// TestDeferWakePreflightErrorSkipsTheSweep: the sweep is advisory, so a
// preflight that fails for any other reason skips it with the same warning a
// failed sweep prints, and the ready read that called it goes on.
func TestDeferWakePreflightErrorSkipsTheSweep(t *testing.T) {
	store, mock, counts := vhCountedStore(t)
	expectAutocommit(mock, 1)
	mock.ExpectQuery(deferWakePreflight).
		WillReturnError(&mysql.MySQLError{Number: 1064, Message: "syntax error"})

	stderr := captureOpenStderr(t, func() { store.wakeExpiredDefers(context.Background()) })

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := counts.Snapshot(); got.Begin != 0 || got.Total() != 2 {
		t.Errorf("a failed preflight went on to send %+v, want the autocommit read and the preflight only", got)
	}
	if !strings.Contains(stderr, "warning: defer-wake sweep skipped") || !strings.Contains(stderr, "syntax error") {
		t.Errorf("a failed preflight printed %q, want the sweep-skipped warning with its error", stderr)
	}
}

// TestDeferWakeSweepStrictReadOnlySendsNothing: a strict --readonly open never
// sweeps, so it does not ask whether anything is due either.
func TestDeferWakeSweepStrictReadOnlySendsNothing(t *testing.T) {
	store, mock, counts := vhCountedStore(t)
	store.readOnly = true

	store.wakeExpiredDefers(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := counts.Snapshot().Total(); got != 0 {
		t.Errorf("a strict read-only sweep sent %d statements, want none", got)
	}
}

// TestDeferWakePreflightKeepsATransactionWithoutAutocommit: with autocommit off
// a bare SELECT would hold its session on a stale snapshot, so the preflight
// runs in a read transaction, which rolls back. Nothing due still commits
// nothing.
func TestDeferWakePreflightKeepsATransactionWithoutAutocommit(t *testing.T) {
	store, mock, counts := vhCountedStore(t)
	expectAutocommit(mock, 0)
	mock.ExpectBegin()
	expectDeferWakePreflight(mock, 0, 0)
	mock.ExpectRollback()

	store.wakeExpiredDefers(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := counts.Snapshot(); got.Begin != 1 || got.Rollback != 1 || got.Commit != 0 {
		t.Errorf("the preflight without autocommit ran %+v, want one rolled-back read transaction", got)
	}
}

// TestDeferWakePreflightStaleAnswers covers the preflight's one hazard: it
// reads outside the sweep's write transaction, so its answer can be out of date
// by the time the write would run. The script stands in for a concurrent
// writer by changing what the database answers between the two points.
func TestDeferWakePreflightStaleAnswers(t *testing.T) {
	t.Run("a row that becomes due after a negative preflight is woken by the next sweep", func(t *testing.T) {
		store, mock, counts := vhCountedStore(t)
		expectAutocommit(mock, 1)
		// First sweep: nothing is due yet.
		expectDeferWakePreflight(mock, 0, 0)
		// A defer date passes here. The next sweep sees it.
		expectDeferWakePreflight(mock, 0, 1)
		expectWispWake(mock, "bd-wisp-1")

		store.wakeExpiredDefers(context.Background())
		if got := counts.Snapshot(); got.Begin != 0 {
			t.Fatalf("the negative sweep opened a transaction: %+v", got)
		}
		store.wakeExpiredDefers(context.Background())

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("the next sweep did not wake the row: %v", err)
		}
	})

	t.Run("a row rescued after a positive preflight and before the snapshot is not touched", func(t *testing.T) {
		store, mock, counts := vhCountedStore(t)
		expectAutocommit(mock, 1)
		expectDeferWakePreflight(mock, 0, 1)
		// The wisp is undeferred here. The write transaction's own snapshot
		// repeats the predicate and finds nothing.
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM issues WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectQuery(`SELECT id FROM wisps WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectCommit()

		store.wakeExpiredDefers(context.Background())

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		if got := counts.Snapshot(); got.Exec != 0 {
			t.Errorf("a sweep whose row was rescued wrote %d times, want no write", got.Exec)
		}
	})

	// This case pins how the sweep treats an UPDATE that affects no row: it
	// records no event and no journal row, and the transaction commits. It does
	// not show that Dolt makes a rescue committed by another transaction after
	// the write's snapshot visible to that UPDATE; a mock cannot. Such a
	// concurrent rescue is caught at commit instead, because the UPDATE rewrites
	// row_lock (see issueops.ExpiredDefersDue).
	t.Run("an UPDATE that affects no row records nothing", func(t *testing.T) {
		store, mock, counts := vhCountedStore(t)
		expectAutocommit(mock, 1)
		expectDeferWakePreflight(mock, 0, 1)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM issues WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectQuery(`SELECT id FROM wisps WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-wisp-1"))
		mock.ExpectExec(`UPDATE wisps SET status = 'open', defer_until = NULL`).
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "bd-wisp-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()

		store.wakeExpiredDefers(context.Background())

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		if got := counts.Snapshot(); got.Exec != 1 {
			t.Errorf("a sweep whose UPDATE affected no row wrote %d times, want the UPDATE alone", got.Exec)
		}
	})
}

// TestDeferWakePreflightIsInsideTheCircuitBoundary: the preflight and the
// write it may open share one circuit-write boundary. A preflight that
// succeeds must not reset the breaker while the write is still to come, and an
// open circuit must stop the sweep before it sends anything.
func TestDeferWakePreflightIsInsideTheCircuitBoundary(t *testing.T) {
	t.Run("a write that loses its commit response after a positive preflight trips the breaker", func(t *testing.T) {
		t.Setenv("BEADS_TEST_MODE", "")
		store, mock, _ := vhCountedStore(t)
		store.breaker = newTestCircuitBreaker(t)
		for range circuitFailureThreshold - 1 {
			store.breaker.RecordFailure()
		}
		expectAutocommit(mock, 1)
		expectDeferWakePreflight(mock, 0, 1)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM issues WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectQuery(`SELECT id FROM wisps WHERE status = 'deferred'`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectCommit().WillReturnError(testConnectionLoss)

		stderr := captureOpenStderr(t, func() { store.wakeExpiredDefers(context.Background()) })

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		if state := store.breaker.State(); state != circuitOpen {
			t.Fatalf("breaker after a lost commit response = %q, want %q: the preflight's success reset the failures the write should have added to", state, circuitOpen)
		}
		if !strings.Contains(stderr, "defer-wake sweep skipped") {
			t.Errorf("a lost commit response printed %q, want the sweep-skipped warning", stderr)
		}
	})

	t.Run("an open circuit sends nothing", func(t *testing.T) {
		t.Setenv("BEADS_TEST_MODE", "")
		store, mock, counts := vhCountedStore(t)
		store.breaker = newTestCircuitBreaker(t)
		for range circuitFailureThreshold {
			store.breaker.RecordFailure()
		}

		stderr := captureOpenStderr(t, func() { store.wakeExpiredDefers(context.Background()) })

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		if n := deferWakeRoundTrips(counts.Snapshot()); n != 0 {
			t.Errorf("a sweep behind an open circuit sent %d statements, want none", n)
		}
		if stderr != "" {
			t.Errorf("a sweep behind an open circuit printed %q, want nothing", stderr)
		}
	})
}
