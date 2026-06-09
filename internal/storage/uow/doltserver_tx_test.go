package uow

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// matchHasPending matches the dolt_status working-set check issued by
// issueops.HasPendingChanges. The guard in (*doltServerTx).Commit runs this
// BEFORE deciding whether to issue DOLT_COMMIT('-Am', …).
const matchHasPending = `SELECT COUNT\(\*\) FROM dolt_status`

// matchDoltCommit matches the empty-commit-prone CALL DOLT_COMMIT('-Am', …).
const matchDoltCommit = `CALL DOLT_COMMIT\('-Am'`

// newMockConn returns a real *sql.Conn backed by sqlmock, plus the mock. The
// guard calls issueops.HasPendingChanges(ctx, t.conn) and then t.conn.ExecContext
// directly on this connection, so a sqlmock-backed *sql.Conn exercises the exact
// production code path.
func newMockConn(t *testing.T) (*sql.Conn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = db.Close()
	})
	return conn, mock
}

// TestDoltServerTxCommitSkipsEmptyCommit is the discriminating regression test
// for the high-frequency "nothing to commit" Dolt warning + CPU churn driven by
// the proxied-server UnitOfWork commit path (internal/storage/uow/doltserver_tx.go).
//
// The bug: (*doltServerTx).Commit issued CALL DOLT_COMMIT('-Am', ?) UNCONDITIONALLY.
// When an idempotent write (same-value REPLACE INTO metadata, same-actor 0-row
// CAS re-claim) staged nothing, the '-Am' commit was rejected server-side with
// "nothing to commit", logged as a warning and burning Dolt CPU evaluating the
// empty commit — at the gascity reconciler's per-tick cadence this floods dolt.log.
//
// The fix gates the COMMIT on issueops.HasPendingChanges (the global working-set
// check, which mirrors what '-Am' commits). This test asserts:
//   - no-op (HasPendingChanges -> 0 rows): DOLT_COMMIT is NOT issued, Commit -> nil.
//   - real write (HasPendingChanges -> >0): DOLT_COMMIT IS issued, Commit -> nil.
//
// It FAILS if the guard is reverted: an unconditional Commit would issue
// DOLT_COMMIT in the no-op case, which has no matching sqlmock expectation, so
// ExecContext returns an "unexpected" error -> Commit returns non-nil -> test fails.
func TestDoltServerTxCommitSkipsEmptyCommit(t *testing.T) {
	t.Run("no pending changes -> skips DOLT_COMMIT", func(t *testing.T) {
		conn, mock := newMockConn(t)

		// HasPendingChanges returns 0: the working set is clean (idempotent no-op).
		mock.ExpectQuery(matchHasPending).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		// Deliberately NO ExpectExec for DOLT_COMMIT: if the code issues it anyway
		// (guard reverted), sqlmock returns an "unexpected Exec" error and Commit
		// returns non-nil, failing this test.

		tx := &doltServerTx{conn: conn}
		if err := tx.Commit(context.Background(), "bd: noop"); err != nil {
			t.Fatalf("Commit on a clean working set should be a no-op nil, got: %v", err)
		}
		if !tx.done {
			t.Error("Commit must mark the tx done even when it skips the empty commit")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet/unexpected sqlmock expectations: %v", err)
		}
	})

	t.Run("pending changes -> issues DOLT_COMMIT", func(t *testing.T) {
		conn, mock := newMockConn(t)

		// HasPendingChanges returns >0: a real change is staged-and-committed by -Am.
		mock.ExpectQuery(matchHasPending).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
		mock.ExpectExec(matchDoltCommit).
			WithArgs("bd: real write").
			WillReturnResult(sqlmock.NewResult(0, 1))

		tx := &doltServerTx{conn: conn}
		if err := tx.Commit(context.Background(), "bd: real write"); err != nil {
			t.Fatalf("Commit with pending changes should succeed, got: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("expected DOLT_COMMIT to be issued for a real change: %v", err)
		}
	})

	t.Run("benign nothing-to-commit from DOLT_COMMIT is swallowed", func(t *testing.T) {
		conn, mock := newMockConn(t)

		// dolt_status counted the working set as dirty (e.g. a row rewritten to its
		// existing value), so the guard lets the commit through, but Dolt still
		// reports "nothing to commit". That must be swallowed, not surfaced.
		mock.ExpectQuery(matchHasPending).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectExec(matchDoltCommit).
			WithArgs("bd: rewrite same value").
			WillReturnError(errors.New("nothing to commit"))

		tx := &doltServerTx{conn: conn}
		if err := tx.Commit(context.Background(), "bd: rewrite same value"); err != nil {
			t.Fatalf("benign nothing-to-commit should be swallowed, got: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet/unexpected sqlmock expectations: %v", err)
		}
	})

	t.Run("real DOLT_COMMIT error is propagated", func(t *testing.T) {
		conn, mock := newMockConn(t)

		mock.ExpectQuery(matchHasPending).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectExec(matchDoltCommit).
			WithArgs("bd: boom").
			WillReturnError(errors.New("dolt: disk full"))

		tx := &doltServerTx{conn: conn}
		err := tx.Commit(context.Background(), "bd: boom")
		if err == nil {
			t.Fatal("a genuine DOLT_COMMIT error must be propagated, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet/unexpected sqlmock expectations: %v", err)
		}
	})
}

// TestDoltServerTxCommitAlreadyDone verifies the existing double-commit guard is
// preserved by the change (no new query/exec is issued on a second Commit).
func TestDoltServerTxCommitAlreadyDone(t *testing.T) {
	conn, mock := newMockConn(t)
	// No expectations at all: a done tx must touch the connection zero times.
	tx := &doltServerTx{conn: conn, done: true}
	err := tx.Commit(context.Background(), "bd: late")
	if err == nil {
		t.Fatal("Commit on an already-done tx must error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a done-tx Commit must not query/exec anything: %v", err)
	}
}
