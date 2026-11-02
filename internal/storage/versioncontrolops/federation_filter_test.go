package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// GH#7890: the excluded-type DELETE is the heart of the federation filter, so
// it must fail closed — swallowing the error would publish the unfiltered tip,
// the exact leak the filtering exists to prevent. These tests pin the
// fail-closed contract and the rows-affected/wisp-SQL mapping of
// DeleteExcludedIssues; the backend integration tests (embeddeddolt and dolt)
// pin the rest of the filtered-push failure handling around it.

var matchDeleteByIssueType = regexp.QuoteMeta("DELETE FROM issues WHERE issue_type = ?")
var matchDeleteEphemeral = regexp.QuoteMeta("DELETE FROM issues WHERE ephemeral = 1")

func TestDeleteExcludedIssuesFailClosedOnError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// The DELETE fails mid-flight (dropped connection, lock timeout, ...).
	deleteErr := errors.New("Error 1317: query execution was interrupted")
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnError(deleteErr)

	if _, err := DeleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}); err == nil {
		t.Fatal("DeleteExcludedIssues swallowed the DELETE error, want fail-closed")
	} else {
		if !errors.Is(err, deleteErr) {
			t.Errorf("error %v does not wrap the DELETE failure", err)
		}
		if !strings.Contains(err.Error(), "delete excluded issues (chore)") {
			t.Errorf("error %q does not name the failing exclude type", err)
		}
	}
	// The second exclude type's DELETE must never be issued: after a failure
	// the push aborts instead of publishing anything. sqlmock fails the test
	// if the un-expected wisp DELETE runs, and ExpectationsWereMet proves the
	// chore one did.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

// A failure partway through the exclude list must still abort: the rows
// already deleted stay deleted only on the temporary staging branch, and the
// caller discards it once the error propagates.
func TestDeleteExcludedIssuesFailClosedMidList(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 2))
	deleteErr := errors.New("Error 1205: lock wait timeout exceeded")
	mock.ExpectExec(matchDeleteEphemeral).
		WillReturnError(deleteErr)

	if _, err := DeleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}); !errors.Is(err, deleteErr) {
		t.Fatalf("DeleteExcludedIssues error = %v, want the wisp DELETE failure wrapped", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

func TestDeleteExcludedIssuesReportsRowsAffected(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// chore deletes two rows; the wisp safety net (ephemeral = 1) finds none
	// because wisps live in dolt_ignore'd tables. The expectations double as
	// the SQL mapping assertion: a literal issue_type = 'wisp' DELETE would
	// not match the ephemeral expectation and sqlmock would error.
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(matchDeleteEphemeral).
		WillReturnResult(sqlmock.NewResult(0, 0))

	deleted, err := DeleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"})
	if err != nil {
		t.Fatalf("DeleteExcludedIssues: %v", err)
	}
	if !deleted {
		t.Error("deleted = false, want true after the chore DELETE removed rows")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

func TestDeleteExcludedIssuesNothingMatchedReportsFalse(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Nothing to filter: the caller must not mint the filtered commit F (nor
	// its revert R), and must push nothing but the ordinary tip.
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 0))

	deleted, err := DeleteExcludedIssues(context.Background(), db, []string{"chore"})
	if err != nil {
		t.Fatalf("DeleteExcludedIssues: %v", err)
	}
	if deleted {
		t.Error("deleted = true, want false when no rows matched")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}
