package schema

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// These tests pin the lifecycle of the session dolt_transaction_commit
// suspension a needed migration pass performs (gastownhall/beads#4470, see
// transaction_commit.go):
//
//   - an enabled session is SET to 0 on entry and restored on exit,
//   - a converged open issues no new statement (no read either),
//   - a failed read or SET fails the pass instead of continuing with
//     unknown commit semantics,
//   - a dirty-guard refusal still surfaces as *DirtyTablesError with the
//     suspension restored,
//   - a restoration failure joins the pass's own error without masking it,
//     and is reported even when the pass itself succeeded.
//
// sqlmock cannot model context cancellation (it ignores ctx), so the
// cancellation-independent restoration contract — a canceled caller must
// still get the session restored on a usable connection — is covered by
// TestEmbeddedMigrateRestoresTransactionCommitAfterFault in
// internal/storage/embeddeddolt.

// TestMigrateUpSuspendsAndRestoresTransactionCommitForEnabledSession walks a
// full needed pass on a session whose commit-semantics setting is enabled:
// the SET to 0 straddles the pass (ordered sqlmock expectations place it
// before the pass's first statement) and the restoration is the pass's last
// statement.
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

// TestMigrateUpDoesNotSuspTransactionCommitWhenNoWorkNeeded asserts the
// converged open stays free of new statements: migrationWorkNeeded returns
// false, so the suspension probe is never issued (it would surface as an
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

// expectWorkNeededPassPrefix mocks MigrateUp up to the point the pass has
// decided work is needed and is about to probe the session's commit
// semantics.
func expectWorkNeededPassPrefix(mock sqlmock.Sqlmock) {
	cursor := LatestVersion() - 1
	expectIgnorePatternSeedNoop(mock, cursor)
	expectIgnoredCursorHealNoop(mock)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", cursor)
}

// TestMigrateUpTransactionCommitReadErrorFailsPass pins that a probe failure
// aborts the pass instead of leaving commit semantics unknown (a pass run
// anyway could silently commit on boundaries it does not own).
func TestMigrateUpTransactionCommitReadErrorFailsPass(t *testing.T) {
	db, mock := newMockDB(t)

	expectWorkNeededPassPrefix(mock)
	readErr := errors.New("unknown system variable: 'dolt_transaction_commit'")
	mock.ExpectQuery(regexp.QuoteMeta(readSessionTransactionCommit)).
		WillReturnError(readErr)

	_, err := MigrateUp(context.Background(), db)
	if err == nil {
		t.Fatal("MigrateUp() error = nil, want the probe failure")
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("MigrateUp() error = %v, want the probe failure as the cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpTransactionCommitSetErrorFailsPass pins the same for the SET
// itself: the pass never starts, and no restoration is attempted (the
// session was never changed).
func TestMigrateUpTransactionCommitSetErrorFailsPass(t *testing.T) {
	db, mock := newMockDB(t)

	expectWorkNeededPassPrefix(mock)
	mock.ExpectQuery(regexp.QuoteMeta(readSessionTransactionCommit)).
		WillReturnRows(sqlmock.NewRows([]string{"@@SESSION.dolt_transaction_commit"}).AddRow(1))
	setErr := errors.New("Error 1234: variable is read only")
	mock.ExpectExec(regexp.QuoteMeta(clearSessionTransactionCommit)).
		WillReturnError(setErr)

	_, err := MigrateUp(context.Background(), db)
	if err == nil {
		t.Fatal("MigrateUp() error = nil, want the SET failure")
	}
	if !errors.Is(err, setErr) {
		t.Fatalf("MigrateUp() error = %v, want the SET failure as the cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpRestoresTransactionCommitAfterDirtyGuardRefusal pins the
// mid-pass exit: the dirty-table guard refuses the pass, its own error stays
// observable as the typed *DirtyTablesError (not swallowed, not masked), and
// the ordered expectations prove the entry SET and exit restoration straddle
// the refusal.
func TestMigrateUpRestoresTransactionCommitAfterDirtyGuardRefusal(t *testing.T) {
	db, mock := newMockDB(t)

	expectDirtyGuardRefusalAtSetting(t, mock, 1, nil)

	_, err := MigrateUp(context.Background(), db)
	if err == nil {
		t.Fatal("MigrateUp() error = nil, want *DirtyTablesError")
	}
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("MigrateUp() error = %v (%T), want *DirtyTablesError", err, err)
	}
	// A successful restoration must not contaminate the pass's own error.
	if strings.Contains(err.Error(), "migration pass cleanup") {
		t.Fatalf("MigrateUp() error = %v, want the pass's own error alone (restoration succeeded)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpJoinsRestoreFailureOntoDirtyGuardError covers the pass that
// fails AND whose restoration fails: both causes must be observable, the
// dirty-guard refusal still classifiable via errors.As, and no altered
// reusable session slipped out quietly.
func TestMigrateUpJoinsRestoreFailureOntoDirtyGuardError(t *testing.T) {
	db, mock := newMockDB(t)

	restoreErr := errors.New("driver: bad connection")
	expectDirtyGuardRefusalAtSetting(t, mock, 1, restoreErr)

	_, err := MigrateUp(context.Background(), db)
	if err == nil {
		t.Fatal("MigrateUp() error = nil, want dirty-guard + restore failures")
	}
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("MigrateUp() error = %v (%T), want the dirty-guard refusal preserved", err, err)
	}
	if !errors.Is(err, restoreErr) {
		t.Fatalf("MigrateUp() error = %v, want the restore failure joined onto it", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpReportsRestoreFailureAfterSuccessfulPass pins that a failed
// restoration is reported even when the pass itself completes: the count of
// applied migrations still describes the pass, but the caller gets an error,
// not a silent altered session.
func TestMigrateUpReportsRestoreFailureAfterSuccessfulPass(t *testing.T) {
	db, mock := newMockDB(t)

	restoreErr := errors.New("driver: bad connection")
	expectOnePendingMigrationAtSetting(t, mock, 1, restoreErr)

	applied, err := MigrateUp(context.Background(), db)
	if err == nil {
		t.Fatal("MigrateUp() error = nil, want the restore failure reported")
	}
	if applied != 1 {
		t.Fatalf("MigrateUp() applied = %d, want 1 (the pass completed; only the restoration failed)", applied)
	}
	if !errors.Is(err, restoreErr) {
		t.Fatalf("MigrateUp() error = %v, want the restore failure as the cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
