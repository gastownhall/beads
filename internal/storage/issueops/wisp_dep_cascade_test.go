package issueops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestDeleteWispFromDependenciesCascadesWispDependencies verifies that deleting
// a wisp removes its rows from BOTH dependency tables: rows in `dependencies`
// that target it, plus its `wisp_dependencies` rows in both directions — its
// own outgoing rows (issue_id: step-ordering blocks, parent-child) and rows
// that target it (depends_on_wisp_id). Regression for gastownhall/beads#4673:
// only the `dependencies` table was cleaned, so every mol burn / wisp GC leaked
// its step-dependency rows as permanent orphans (~26 rows per patrol-wisp burn
// observed in production).
func TestDeleteWispFromDependenciesCascadesWispDependencies(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dependencies WHERE depends_on_wisp_id = ?")).
		WithArgs("hq-wisp-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM wisp_dependencies WHERE issue_id = ? OR depends_on_wisp_id = ?")).
		WithArgs("hq-wisp-1", "hq-wisp-1").
		WillReturnResult(sqlmock.NewResult(0, 26))

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := DeleteWispFromDependenciesInTx(context.Background(), tx, "hq-wisp-1"); err != nil {
		t.Fatalf("DeleteWispFromDependenciesInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestDeleteWispsFromDependenciesCascadesWispDependencies is the batch-form
// counterpart of TestDeleteWispFromDependenciesCascadesWispDependencies.
func TestDeleteWispsFromDependenciesCascadesWispDependencies(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dependencies WHERE depends_on_wisp_id IN (?,?)")).
		WithArgs("w-1", "w-2").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM wisp_dependencies WHERE issue_id IN (?,?) OR depends_on_wisp_id IN (?,?)")).
		WithArgs("w-1", "w-2", "w-1", "w-2").
		WillReturnResult(sqlmock.NewResult(0, 52))

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := DeleteWispsFromDependenciesInTx(context.Background(), tx, []string{"w-1", "w-2"}); err != nil {
		t.Fatalf("DeleteWispsFromDependenciesInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestDeleteWispsFromDependenciesEmptyIsNoOp verifies the batch helper issues
// no SQL for an empty id list.
func TestDeleteWispsFromDependenciesEmptyIsNoOp(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := DeleteWispsFromDependenciesInTx(context.Background(), tx, nil); err != nil {
		t.Fatalf("DeleteWispsFromDependenciesInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
