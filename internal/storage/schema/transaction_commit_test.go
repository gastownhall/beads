package schema

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectTransactionCommitRead mocks suspendTransactionCommit's read of the
// session's @@dolt_transaction_commit, which a needed migration pass issues
// right after migrationWorkNeeded. 0 (the default) means no SET follows.
func expectTransactionCommitRead(mock sqlmock.Sqlmock, value int) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT @@SESSION.dolt_transaction_commit")).
		WillReturnRows(sqlmock.NewRows([]string{"@@SESSION.dolt_transaction_commit"}).AddRow(value))
}

func expectTransactionCommitSet(mock sqlmock.Sqlmock, value int) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta("SET @@SESSION.dolt_transaction_commit = " + strconv.Itoa(value)))
}

func TestSuspendTransactionCommitLeavesADisabledSessionAlone(t *testing.T) {
	db, mock := newMockDB(t)
	expectTransactionCommitRead(mock, 0)

	restore, err := suspendTransactionCommit(context.Background(), db)
	if err != nil {
		t.Fatalf("suspendTransactionCommit() error = %v", err)
	}
	passErr := errors.New("pass failed")
	if got := restore(passErr); got != passErr {
		t.Fatalf("restore(passErr) = %v, want passErr unchanged", got)
	}
	if err := restore(nil); err != nil {
		t.Fatalf("restore(nil) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (no SET may be issued): %v", err)
	}
}

// The pass's context may be canceled by the time it returns; the restore must
// not ride it, or a canceled open would leave the session's setting off.
func TestSuspendTransactionCommitRestoresOnACanceledContext(t *testing.T) {
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	expectTransactionCommitRead(mock, 1)
	expectTransactionCommitSet(mock, 0).WillReturnResult(sqlmock.NewResult(0, 0))
	expectTransactionCommitSet(mock, 1).WillReturnResult(sqlmock.NewResult(0, 0))

	restore, err := suspendTransactionCommit(ctx, conn)
	if err != nil {
		t.Fatalf("suspendTransactionCommit() error = %v", err)
	}
	cancel()
	if err := restore(nil); err != nil {
		t.Fatalf("restore(nil) after cancel = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A successful pass whose restore fails must report the failure, and the
// pinned connection must not go back to the pool with the setting off.
func TestSuspendTransactionCommitDiscardsAnUnrestorableConn(t *testing.T) {
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()
	ctx := context.Background()
	restoreErr := errors.New("connection lost")
	expectTransactionCommitRead(mock, 1)
	expectTransactionCommitSet(mock, 0).WillReturnResult(sqlmock.NewResult(0, 0))
	expectTransactionCommitSet(mock, 1).WillReturnError(restoreErr)
	// discardConn hands driver.ErrBadConn to database/sql, which closes the
	// driver connection: this Close is the proof it was discarded.
	mock.ExpectClose()

	restore, err := suspendTransactionCommit(ctx, conn)
	if err != nil {
		t.Fatalf("suspendTransactionCommit() error = %v", err)
	}
	if err := restore(nil); !errors.Is(err, restoreErr) {
		t.Fatalf("restore(nil) = %v, want the restore failure", err)
	}
	if err := conn.PingContext(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("pinned conn after a failed restore: Ping = %v, want sql.ErrConnDone (discarded)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
