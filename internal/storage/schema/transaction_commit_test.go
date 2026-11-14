package schema

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
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
// fails, both causes stay observable, on one line with the pass's error first
// so a last-line-only capture cannot drop the migration diagnostic.
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
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Fatalf("migrateUpAfterReconcile() error is multi-line, want one line: %q", msg)
	}
	passIdx := strings.Index(msg, errPass.Error())
	restoreIdx := strings.Index(msg, "restoring @@dolt_transaction_commit")
	if passIdx < 0 || restoreIdx < passIdx {
		t.Fatalf("migrateUpAfterReconcile() error = %q, want the pass error first, then the restore failure", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
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

// TestMigrateUpSuspendsAndRestoresTransactionCommitForEnabledSession walks a
// full needed pass on a session whose setting is enabled: the ordered
// expectations place the SET to 0 before the pass's first statement and the
// restoration after its last.
func TestMigrateUpSuspendsAndRestoresTransactionCommitForEnabledSession(t *testing.T) {
	db, mock := newMockDB(t)

	expectOnePendingMigrationAtSetting(t, mock, 1, nil)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUp() applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpDoesNotSuspendTransactionCommitWhenNoWorkNeeded: a converged
// open issues no new statement, not even the read (it would surface as an
// unexpected query).
func TestMigrateUpDoesNotSuspendTransactionCommitWhenNoWorkNeeded(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnorePatternSeedNoop(mock, LatestVersion())
	expectIgnoredCursorHealNoop(mock)
	expectNoMigrationWorkNeeded(mock)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUp() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpTransactionCommitSetErrorFailsPass: a failed SET aborts the
// pass before it starts, and no restoration is attempted (the session was
// never changed).
func TestMigrateUpTransactionCommitSetErrorFailsPass(t *testing.T) {
	db, mock := newMockDB(t)

	cursor := LatestVersion() - 1
	expectIgnorePatternSeedNoop(mock, cursor)
	expectIgnoredCursorHealNoop(mock)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", cursor)
	expectTransactionCommitRead(mock, 1)
	setErr := errors.New("Error 1234: variable is read only")
	expectSetTransactionCommit(mock, "0").WillReturnError(setErr)

	_, err := MigrateUp(context.Background(), db)
	if !errors.Is(err, setErr) {
		t.Fatalf("MigrateUp() error = %v, want the SET failure as the cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpRestoresTransactionCommitAfterDirtyGuardRefusal: the
// dirty-table guard's refusal stays observable as the typed
// *DirtyTablesError, and the entry SET and exit restoration straddle it.
func TestMigrateUpRestoresTransactionCommitAfterDirtyGuardRefusal(t *testing.T) {
	db, mock := newMockDB(t)

	expectDirtyGuardRefusalAtSetting(t, mock, 1, nil)

	_, err := MigrateUp(context.Background(), db)
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("MigrateUp() error = %v (%T), want *DirtyTablesError", err, err)
	}
	// A successful restoration must not contaminate the pass's own error.
	if strings.Contains(err.Error(), "restoring @@dolt_transaction_commit") {
		t.Fatalf("MigrateUp() error = %v, want the pass's own error alone (restoration succeeded)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpJoinsRestoreFailureOntoDirtyGuardError: a refused pass whose
// restoration also fails reports both, with the refusal still classifiable.
func TestMigrateUpJoinsRestoreFailureOntoDirtyGuardError(t *testing.T) {
	db, mock := newMockDB(t)

	restoreErr := errors.New("driver: bad connection")
	expectDirtyGuardRefusalAtSetting(t, mock, 1, restoreErr)

	_, err := MigrateUp(context.Background(), db)
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("MigrateUp() error = %v (%T), want the dirty-guard refusal preserved", err, err)
	}
	if !errors.Is(err, restoreErr) {
		t.Fatalf("MigrateUp() error = %v, want the restore failure joined onto it", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("MigrateUp() error is multi-line, want one line: %q", err.Error())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpReportsRestoreFailureAfterSuccessfulPass: a failed restoration
// is reported even when the pass itself completed; the applied count still
// describes the pass.
func TestMigrateUpReportsRestoreFailureAfterSuccessfulPass(t *testing.T) {
	db, mock := newMockDB(t)

	restoreErr := errors.New("driver: bad connection")
	expectOnePendingMigrationAtSetting(t, mock, 1, restoreErr)

	applied, err := MigrateUp(context.Background(), db)
	if !errors.Is(err, restoreErr) {
		t.Fatalf("MigrateUp() error = %v, want the restore failure reported", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUp() applied = %d, want 1 (the pass completed; only the restoration failed)", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
