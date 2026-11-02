package schema

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectMainMigrationWorkNeeded mocks migrationWorkNeeded finding the main
// cursor behind latest, which short-circuits the remaining probes.
func expectMainMigrationWorkNeeded(mock sqlmock.Sqlmock) {
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", 42)
}

func expectSetTransactionCommit(mock sqlmock.Sqlmock, value string) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta("SET @@SESSION.dolt_transaction_commit = " + value))
}

// TestMigrationPassRestoresTransactionCommitAfterFailure_4470: a pass that
// suspended the setting and then failed restores it, and returns the pass's
// own error unchanged.
func TestMigrationPassRestoresTransactionCommitAfterFailure_4470(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	errPass := errors.New("dolt_status read failed")
	expectMainMigrationWorkNeeded(mock)
	expectTransactionCommitRead(mock, 1)
	expectSetTransactionCommit(mock, "0").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").WillReturnError(errPass)
	expectSetTransactionCommit(mock, "1").WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = migrateUpAfterReconcile(context.Background(), db, false)
	if !errors.Is(err, errPass) {
		t.Fatalf("migrateUpAfterReconcile() error = %v, want the pass error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("setting not restored: %v", err)
	}
}

// TestMigrationPassJoinsRestoreFailureToPassError_4470: when the restore also
// fails, both causes stay observable.
func TestMigrationPassJoinsRestoreFailureToPassError_4470(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	errPass := errors.New("dolt_status read failed")
	errRestore := errors.New("connection lost")
	expectMainMigrationWorkNeeded(mock)
	expectTransactionCommitRead(mock, 1)
	expectSetTransactionCommit(mock, "0").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").WillReturnError(errPass)
	expectSetTransactionCommit(mock, "1").WillReturnError(errRestore)

	_, err = migrateUpAfterReconcile(context.Background(), db, false)
	if !errors.Is(err, errPass) || !errors.Is(err, errRestore) {
		t.Fatalf("migrateUpAfterReconcile() error = %v, want both the pass and restore errors", err)
	}
}

// TestMigrationPassReadFailureIsFatal_4470: a pass never runs with unknown
// commit semantics.
func TestMigrationPassReadFailureIsFatal_4470(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	errRead := errors.New("unknown system variable")
	expectMainMigrationWorkNeeded(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT @@SESSION.dolt_transaction_commit")).WillReturnError(errRead)

	_, err = migrateUpAfterReconcile(context.Background(), db, false)
	if !errors.Is(err, errRead) {
		t.Fatalf("migrateUpAfterReconcile() error = %v, want the read error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected statements after the failed read: %v", err)
	}
}

// TestTransactionCommitRestoreFailureDiscardsPinnedConn_4470: a pinned
// session whose setting could not be restored is discarded, not handed back
// with automatic transaction commits silently off.
func TestTransactionCommitRestoreFailureDiscardsPinnedConn_4470(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	defer conn.Close()

	errRestore := errors.New("connection lost")
	expectTransactionCommitRead(mock, 1)
	expectSetTransactionCommit(mock, "0").WillReturnResult(sqlmock.NewResult(0, 0))
	expectSetTransactionCommit(mock, "1").WillReturnError(errRestore)

	restore, err := suspendTransactionCommit(ctx, conn)
	if err != nil {
		t.Fatalf("suspendTransactionCommit() error = %v", err)
	}
	if err := restore(); !errors.Is(err, errRestore) {
		t.Fatalf("restore() error = %v, want the restore error", err)
	}
	if err := conn.PingContext(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("pinned conn after failed restore: PingContext() = %v, want sql.ErrConnDone", err)
	}
}

// TestTransactionCommitRestoreSurvivesCanceledContext_4470: the restore still
// runs when the pass's context was canceled.
func TestTransactionCommitRestoreSurvivesCanceledContext_4470(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	expectTransactionCommitRead(mock, 1)
	expectSetTransactionCommit(mock, "0").WillReturnResult(sqlmock.NewResult(0, 0))
	expectSetTransactionCommit(mock, "1").WillReturnResult(sqlmock.NewResult(0, 0))

	restore, err := suspendTransactionCommit(ctx, db)
	if err != nil {
		t.Fatalf("suspendTransactionCommit() error = %v", err)
	}
	cancel()
	if err := restore(); err != nil {
		t.Fatalf("restore() after cancel error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("setting not restored: %v", err)
	}
}
