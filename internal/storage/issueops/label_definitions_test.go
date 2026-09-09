package issueops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestDefineLabelInTx_ConcurrentCollisionAtInsertIsTranslated is the
// deterministic regression test for the DB-level backstop label_folded's
// UNIQUE constraint adds (migration 0066): a check-then-insert cannot see a
// row a concurrent transaction has not committed yet, so the pre-check
// (rejectLabelCollisionInTx) can pass for BOTH of two racing callers. This
// pins what happens to the loser: its INSERT hits the label_folded UNIQUE
// constraint, and DefineLabelInTx must translate that into the same
// storage.ErrValidation-wrapped, label-naming error the pre-check gives the
// common (non-racing) case -- never a raw, uncaught driver error.
//
// Simulating this deterministically (rather than racing real goroutines
// against a live Dolt server, which cannot reliably force both transactions
// through the pre-check before either commits) is the standard way to test a
// uniqueness constraint's failure path: sqlmock lets the pre-check SELECT
// report "no collision yet" while the INSERT independently reports the
// exact driver error a lost race produces.
func TestDefineLabelInTx_ConcurrentCollisionAtInsertIsTranslated(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	// The pre-check finds nothing -- this caller's own transaction has not
	// seen the concurrent winner's row yet.
	mock.ExpectQuery(`SELECT label FROM label_definitions WHERE label_folded = \?`).
		WithArgs("backend").
		WillReturnError(sql.ErrNoRows)
	// The INSERT is where the race is actually decided: the concurrent
	// winner already committed "backend" (label_folded "backend"), so this
	// loser's INSERT of "Backend" (also folding to "backend") collides on
	// the label_folded UNIQUE constraint.
	mock.ExpectExec(`INSERT INTO label_definitions \(label, label_folded, description, created_by\) VALUES \(\?, \?, \?, \?\)`).
		WithArgs("Backend", "backend", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'backend' for key 'label_definitions.uk_label_definitions_folded'"})

	err := DefineLabelInTx(context.Background(), tx, "Backend", "", "loser")
	if err == nil {
		t.Fatal("expected the lost race to surface an error, got nil")
	}
	if !errors.Is(err, storage.ErrValidation) {
		t.Errorf("expected storage.ErrValidation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Backend") {
		t.Errorf("expected the error to name the label the caller tried to define, got: %v", err)
	}
	// It must read as a NAMED COLLISION, not a bare driver error leaking
	// through -- the whole point of translating it.
	if strings.Contains(err.Error(), "Duplicate entry") || strings.Contains(err.Error(), "1062") {
		t.Errorf("expected the raw driver error to be translated, not passed through, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet SQL expectations: %v", err)
	}
}

// TestDefineLabelInTx_FoldsInGoNotSQL pins the single folding authority:
// strings.ToLower in Go computes both the pre-check's needle and the
// label_folded value the INSERT stores, and no query folds case in SQL
// (no LOWER()). sqlmock's regex match on the exact query text already
// enforces "no LOWER() in the SQL string"; the WithArgs assertions enforce
// that the needle sqlmock sees is the Go-folded value, not the raw label.
func TestDefineLabelInTx_FoldsInGoNotSQL(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	mock.ExpectQuery(`SELECT label FROM label_definitions WHERE label_folded = \?`).
		WithArgs("shouty").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO label_definitions \(label, label_folded, description, created_by\) VALUES \(\?, \?, \?, \?\)`).
		WithArgs("SHOUTY", "shouty", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := DefineLabelInTx(context.Background(), tx, "SHOUTY", "", "tester"); err != nil {
		t.Fatalf("DefineLabelInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet SQL expectations (a LOWER()-in-SQL query, or a raw-cased needle, would not match): %v", err)
	}
}

// TestDefineLabelInTx_CreatedByLengthValidated is the regression test for
// the missing types.CheckFieldLen on created_by/actor: label and
// description both already had it, created_by did not. Setting up NO mock
// expectations and asserting none were touched proves the refusal happens
// BEFORE any SQL runs, matching label's and description's own validation
// order.
func TestDefineLabelInTx_CreatedByLengthValidated(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	overLong := strings.Repeat("a", types.MaxFieldLen+1)
	err := DefineLabelInTx(context.Background(), tx, "backend", "", overLong)
	if err == nil {
		t.Fatal("expected an error for an over-length created_by, got nil")
	}
	if !errors.Is(err, types.ErrFieldTooLong) {
		t.Errorf("expected types.ErrFieldTooLong, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected the length check to refuse before touching SQL, but a query ran: %v", err)
	}
}

// TestUndefineLabelInTx_FoldsInGoNotSQL is UndefineLabelInTx's twin of
// TestDefineLabelInTx_FoldsInGoNotSQL: the DELETE must match on
// label_folded with a Go-folded needle, not LOWER(label) = LOWER(?).
func TestUndefineLabelInTx_FoldsInGoNotSQL(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	mock.ExpectExec(`DELETE FROM label_definitions WHERE label_folded = \?`).
		WithArgs("backend").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := UndefineLabelInTx(context.Background(), tx, "BACKEND"); err != nil {
		t.Fatalf("UndefineLabelInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet SQL expectations (a LOWER()-in-SQL query, or a raw-cased needle, would not match): %v", err)
	}
}
