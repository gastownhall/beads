package issueops

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestAddRemoteIfNotExistsRejectsLeadingDash is the issueops boundary of the
// option-injection fix (GH#7876): registering a federation peer also adds a
// Dolt remote with that name, so an option-shaped peer name must be refused
// before the peer-registration transaction issues any DOLT_REMOTE SQL. The
// sqlmock transaction has zero expectations — a single unexpected exec fails
// the mock, so "no SQL ran" is asserted by the exact refusal text coming
// back instead.
func TestAddRemoteIfNotExistsRejectsLeadingDash(t *testing.T) {
	ctx := context.Background()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	err = AddRemoteIfNotExists(ctx, tx, "--force", "file:///tmp/peer")
	if err == nil {
		t.Fatal("AddRemoteIfNotExists accepted an option-shaped remote name; want the leading-dash refusal")
	}
	const wantErr = `invalid remote name "--force": must not start with '-'`
	if err.Error() != wantErr {
		t.Fatalf("refusal = %q, want exactly %q", err.Error(), wantErr)
	}
}

// TestAddRemoteIfNotExistsValidOperandControl proves a legal name still
// reaches the DOLT_REMOTE statement.
func TestAddRemoteIfNotExistsValidOperandControl(t *testing.T) {
	ctx := context.Background()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	mock.ExpectExec("CALL DOLT_REMOTE\\('add', \\?, \\?\\)").
		WithArgs("1st", "file:///tmp/peer").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := AddRemoteIfNotExists(ctx, tx, "1st", "file:///tmp/peer"); err != nil {
		t.Fatalf("AddRemoteIfNotExists(1st) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}
