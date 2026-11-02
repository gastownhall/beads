package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// GH#7890: the excluded-type DELETE is the heart of the federation filter, so
// it must fail closed: swallowing an error would publish the unfiltered tip,
// the leak the filter exists to prevent. These tests pin the fail-closed
// contract and the rows-affected and wisp SQL mapping of deleteExcludedIssues.

var (
	matchDeleteByIssueType = regexp.QuoteMeta("DELETE FROM issues WHERE issue_type = ?")
	matchDeleteEphemeral   = regexp.QuoteMeta("DELETE FROM issues WHERE ephemeral = 1")
)

func newDeleteExcludedMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func TestDeleteExcludedIssuesFailClosedOnError(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	// The DELETE fails mid-flight (dropped connection, lock timeout, ...).
	deleteErr := errors.New("Error 1317: query execution was interrupted")
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnError(deleteErr)

	_, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"})
	if !errors.Is(err, deleteErr) {
		t.Fatalf("deleteExcludedIssues error = %v, want the DELETE failure wrapped (fail closed)", err)
	}
	if !strings.Contains(err.Error(), "delete excluded type chore") {
		t.Errorf("error %q does not name the failing exclude type", err)
	}
	// The wisp DELETE must never be issued after the failure: sqlmock fails
	// an unexpected Exec, and ExpectationsWereMet proves the chore one ran.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

// A failure partway through the exclude list still aborts: the rows already
// deleted are gone only from the temporary staging branch, which the caller
// discards once the error propagates.
func TestDeleteExcludedIssuesFailClosedMidList(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 2))
	deleteErr := errors.New("Error 1205: lock wait timeout exceeded")
	mock.ExpectExec(matchDeleteEphemeral).
		WillReturnError(deleteErr)

	n, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"})
	if !errors.Is(err, deleteErr) {
		t.Fatalf("deleteExcludedIssues error = %v, want the wisp DELETE failure wrapped", err)
	}
	if n != 0 {
		t.Errorf("removed = %d on failure, want 0", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

// An unreadable rows-affected count fails closed too: the caller could not
// tell whether anything was filtered.
func TestDeleteExcludedIssuesFailClosedOnRowsAffectedError(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	rowsErr := errors.New("rows affected unavailable")
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewErrorResult(rowsErr))

	if _, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}); !errors.Is(err, rowsErr) {
		t.Fatalf("deleteExcludedIssues error = %v, want the RowsAffected failure wrapped", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

func TestDeleteExcludedIssuesReportsRowsAffected(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	// chore deletes two rows; the wisp safety net (ephemeral = 1) deletes one.
	// The expectations double as the SQL mapping assertion: a literal
	// issue_type = 'wisp' DELETE would not match the ephemeral expectation.
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(matchDeleteEphemeral).
		WillReturnResult(sqlmock.NewResult(0, 1))

	n, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"})
	if err != nil {
		t.Fatalf("deleteExcludedIssues: %v", err)
	}
	if n != 3 {
		t.Errorf("removed = %d, want 3", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

func TestDeleteExcludedIssuesNothingMatchedReportsZero(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	// Nothing to filter: the caller must not mint the filtered commit or its
	// revert, and pushes the ordinary tip.
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 0))

	n, err := deleteExcludedIssues(context.Background(), db, []string{"chore"})
	if err != nil {
		t.Fatalf("deleteExcludedIssues: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0 when no rows matched", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}
