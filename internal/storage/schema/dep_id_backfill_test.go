package schema

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
)

// TestRekeyDependencyTableRewritesOnlyDivergentRows verifies the backfill that
// converges existing rows after the #4259 fix: it re-keys a row whose id is not
// the deterministic value, and leaves an already-deterministic row untouched.
func TestRekeyDependencyTableRewritesOnlyDivergentRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// id column present.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Two rows: one carrying a legacy random id, one already deterministic.
	randomRow := "random-uuid-aaaa"
	deterministicRow := depid.New("c", "d")
	mock.ExpectQuery(`SELECT id, issue_id, COALESCE\(.*\) FROM dependencies`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "target"}).
			AddRow(randomRow, "a", "b").
			AddRow(deterministicRow, "c", "d"))

	// Only the divergent row is re-keyed, to its deterministic value.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE dependencies SET id = ? WHERE id = ?")).
		WithArgs(depid.New("a", "b"), randomRow).
		WillReturnResult(sqlmock.NewResult(0, 1))

	wrote, err := rekeyDependencyTable(context.Background(), db, "dependencies")
	if err != nil {
		t.Fatalf("rekeyDependencyTable: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true when a row was re-keyed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyDependencyTableSkipsMissingTable verifies the backfill no-ops cleanly
// when the table/id column is absent (older or partial schema).
func TestRekeyDependencyTableSkipsMissingTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	wrote, err := rekeyDependencyTable(context.Background(), db, "dependencies")
	if err != nil {
		t.Fatalf("rekeyDependencyTable: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when the id column is absent")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyDependencyTableIdempotent verifies that when every row already carries
// its deterministic id, no UPDATE is issued (so re-running is a cheap no-op).
func TestRekeyDependencyTableIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT id, issue_id, COALESCE\(.*\) FROM dependencies`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "target"}).
			AddRow(depid.New("a", "b"), "a", "b"))
	// No ExpectExec: zero UPDATEs expected.

	wrote, err := rekeyDependencyTable(context.Background(), db, "dependencies")
	if err != nil {
		t.Fatalf("rekeyDependencyTable: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when all rows already deterministic")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
