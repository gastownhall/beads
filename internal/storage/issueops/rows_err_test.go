package issueops

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// errMidIteration stands in for a driver error that ends a result set early:
// Rows.Next returns false exactly as it does at end-of-rows, and only
// Rows.Err tells a truncated read from a complete one.
var errMidIteration = errors.New("connection reset mid-read")

// newRowsErrMock returns a sqlmock database whose expectations must all be
// met: no statement may be skipped, and none may run after the failing read
// (an unexpected statement fails the call with sqlmock's own error, which is
// not errMidIteration).
func newRowsErrMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// beginRowsErrTx opens the transaction the *sql.Tx readers take.
func beginRowsErrTx(t *testing.T, db *sql.DB, mock sqlmock.Sqlmock) *sql.Tx {
	t.Helper()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// expectNotWisp answers IsActiveWispInTx's probe with no row, so the
// molecule readers route to issues/dependencies.
func expectNotWisp(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT 1 FROM wisps WHERE id = \?`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
}

func childIDRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"issue_id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

func assertMidIterationFailure(t *testing.T, mock sqlmock.Sqlmock, err error) {
	t.Helper()
	if !errors.Is(err, errMidIteration) {
		t.Fatalf("err = %v, want one wrapping %q: the caller cannot tell a truncated result from a complete one", err, errMidIteration)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations (the failure did not land in the intended read): %v", err)
	}
}

const (
	epicIDsQuery         = `SELECT id FROM issues\s+WHERE issue_type = 'epic'`
	epicDepsQuery        = `AS parent_id, issue_id FROM dependencies\s`
	epicWispDepsQuery    = `AS parent_id, issue_id FROM wisp_dependencies\s`
	epicIssueStatusQuery = `SELECT id, status, COALESCE\(close_reason, ''\) FROM issues WHERE id IN`
	epicWispStatusQuery  = `SELECT id, status, COALESCE\(close_reason, ''\) FROM wisps WHERE id IN`
	moleculeChildQuery   = `SELECT issue_id FROM dependencies\s+WHERE depends_on_issue_id = \? AND type = 'parent-child'`
)

func idRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

func parentChildRows(edges ...[2]string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"parent_id", "issue_id"})
	for _, e := range edges {
		rows.AddRow(e[0], e[1])
	}
	return rows
}

func epicStatusRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "status", "close_reason"})
}

// TestRowIterationReportsMidIterationFailure pins that every row loop in the
// readers below checks Rows.Err, so a driver error part-way through a result
// set is returned instead of an answer computed from the rows read so far.
func TestRowIterationReportsMidIterationFailure(t *testing.T) {
	ctx := context.Background()

	// AC-1: GetEpicsEligibleForClosureInTx.
	runEpics := func(t *testing.T, db *sql.DB, mock sqlmock.Sqlmock) {
		t.Helper()
		got, err := GetEpicsEligibleForClosureInTx(ctx, db)
		if got != nil {
			t.Errorf("got %d epic statuses, want nil", len(got))
		}
		assertMidIterationFailure(t, mock, err)
	}

	t.Run("epics/open-epic-ids", func(t *testing.T) {
		db, mock := newRowsErrMock(t)
		mock.ExpectQuery(epicIDsQuery).
			WillReturnRows(idRows("e-1", "e-2").RowError(1, errMidIteration))
		runEpics(t, db, mock)
	})

	t.Run("epics/open-epic-ids-first-row", func(t *testing.T) {
		db, mock := newRowsErrMock(t)
		mock.ExpectQuery(epicIDsQuery).
			WillReturnRows(idRows("e-1", "e-2").RowError(0, errMidIteration))
		runEpics(t, db, mock)
	})

	t.Run("epics/edges-dependencies", func(t *testing.T) {
		db, mock := newRowsErrMock(t)
		mock.ExpectQuery(epicIDsQuery).WillReturnRows(idRows("e-1"))
		mock.ExpectQuery(epicDepsQuery).
			WillReturnRows(parentChildRows([2]string{"e-1", "c-1"}, [2]string{"e-1", "c-2"}).
				RowError(1, errMidIteration))
		runEpics(t, db, mock)
	})

	t.Run("epics/edges-wisp_dependencies", func(t *testing.T) {
		db, mock := newRowsErrMock(t)
		mock.ExpectQuery(epicIDsQuery).WillReturnRows(idRows("e-1"))
		mock.ExpectQuery(epicDepsQuery).
			WillReturnRows(parentChildRows([2]string{"e-1", "c-1"}))
		mock.ExpectQuery(epicWispDepsQuery).
			WillReturnRows(parentChildRows([2]string{"e-1", "c-2"}, [2]string{"e-1", "c-3"}).
				RowError(1, errMidIteration))
		runEpics(t, db, mock)
	})

	t.Run("epics/child-statuses-issues", func(t *testing.T) {
		db, mock := newRowsErrMock(t)
		mock.ExpectQuery(epicIDsQuery).WillReturnRows(idRows("e-1"))
		mock.ExpectQuery(epicDepsQuery).
			WillReturnRows(parentChildRows([2]string{"e-1", "c-1"}, [2]string{"e-1", "c-2"}))
		mock.ExpectQuery(epicWispDepsQuery).WillReturnRows(parentChildRows())
		mock.ExpectQuery(epicIssueStatusQuery).
			WillReturnRows(epicStatusRows().
				AddRow("c-1", "closed", "").
				AddRow("c-2", "open", "").
				RowError(1, errMidIteration))
		runEpics(t, db, mock)
	})

	t.Run("epics/child-statuses-wisps", func(t *testing.T) {
		db, mock := newRowsErrMock(t)
		mock.ExpectQuery(epicIDsQuery).WillReturnRows(idRows("e-1"))
		mock.ExpectQuery(epicDepsQuery).
			WillReturnRows(parentChildRows([2]string{"e-1", "c-1"}))
		mock.ExpectQuery(epicWispDepsQuery).
			WillReturnRows(parentChildRows([2]string{"e-1", "w-1"}, [2]string{"e-1", "w-2"}))
		mock.ExpectQuery(epicIssueStatusQuery).
			WillReturnRows(epicStatusRows().AddRow("c-1", "closed", ""))
		mock.ExpectQuery(epicWispStatusQuery).
			WillReturnRows(epicStatusRows().
				AddRow("w-1", "closed", "").
				AddRow("w-2", "open", "").
				RowError(1, errMidIteration))
		runEpics(t, db, mock)
	})

	// AC-2: GetMoleculeLastActivityInTx.
	runLastActivity := func(t *testing.T, rows *sqlmock.Rows) {
		t.Helper()
		db, mock := newRowsErrMock(t)
		tx := beginRowsErrTx(t, db, mock)
		expectNotWisp(mock)
		mock.ExpectQuery(moleculeChildQuery).WithArgs("mol-1").WillReturnRows(rows)
		got, err := GetMoleculeLastActivityInTx(ctx, tx, "mol-1")
		if got != nil {
			t.Errorf("got last activity %+v, want nil", got)
		}
		assertMidIterationFailure(t, mock, err)
	}

	t.Run("last-activity/children", func(t *testing.T) {
		runLastActivity(t, childIDRows("c-1", "c-2").RowError(1, errMidIteration))
	})

	t.Run("last-activity/children-first-row", func(t *testing.T) {
		runLastActivity(t, childIDRows("c-1", "c-2").RowError(0, errMidIteration))
	})

	// AC-3: GetMoleculeProgressInTx.
	t.Run("progress/child-statuses", func(t *testing.T) {
		db, mock := newRowsErrMock(t)
		tx := beginRowsErrTx(t, db, mock)
		expectNotWisp(mock)
		mock.ExpectQuery(`SELECT title FROM issues WHERE id = \?`).WithArgs("mol-1").
			WillReturnRows(sqlmock.NewRows([]string{"title"}).AddRow("Molecule"))
		mock.ExpectQuery(moleculeChildQuery).WithArgs("mol-1").
			WillReturnRows(childIDRows("c-1", "c-2"))
		mock.ExpectQuery(`SELECT id, status FROM issues WHERE id IN`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).
				AddRow("c-1", "closed").
				AddRow("c-2", "open").
				RowError(1, errMidIteration))
		got, err := GetMoleculeProgressInTx(ctx, tx, "mol-1")
		if got != nil {
			t.Errorf("got progress %+v, want nil", got)
		}
		assertMidIterationFailure(t, mock, err)
	})

	// AC-4: DeleteIssuesBySourceRepoInTx. No statement is registered after the
	// id read, so a lease delete, journal write, DELETE or is_blocked
	// recompute fails ExpectationsWereMet's ordering.
	runDeleteBySourceRepo := func(t *testing.T, rows *sqlmock.Rows) {
		t.Helper()
		db, mock := newRowsErrMock(t)
		tx := beginRowsErrTx(t, db, mock)
		mock.ExpectQuery(`SELECT id FROM issues WHERE source_repo = \?`).WithArgs("repo-a").
			WillReturnRows(rows)
		n, err := DeleteIssuesBySourceRepoInTx(ctx, tx, "repo-a")
		if n != 0 {
			t.Errorf("deleted = %d, want 0", n)
		}
		assertMidIterationFailure(t, mock, err)
	}

	t.Run("delete-by-source-repo/ids", func(t *testing.T) {
		runDeleteBySourceRepo(t, idRows("i-1", "i-2").RowError(1, errMidIteration))
	})

	t.Run("delete-by-source-repo/ids-first-row", func(t *testing.T) {
		runDeleteBySourceRepo(t, idRows("i-1", "i-2").RowError(0, errMidIteration))
	})
}
