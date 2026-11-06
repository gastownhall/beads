package versioncontrolops

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// The statements Flatten issues, as sqlmock matches them. The final reset of
// main onto the flattened branch must stay soft: a hard reset drops every FK
// on the clone-local tables and discards writes that reached main after the
// snapshot was built (bd-7bpkd).
const (
	flattenSnapshotMsg = "flatten: squash all history into single commit"
)

// expectFlattenReads queues the two reads every flatten starts with: the
// initial commit's hash and the commit count.
func expectFlattenReads(mock sqlmock.Sqlmock, commitCount int) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"commit_hash"}).AddRow("inithash"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(commitCount))
}

// expectFlattenThroughReset queues the reads and the steps through the final
// soft reset of main onto flatten-tmp.
func expectFlattenThroughReset(mock sqlmock.Sqlmock) {
	ok := sqlmock.NewResult(0, 1)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('flatten-tmp')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('flatten-tmp')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).
		WithArgs("inithash").WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")).
		WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'flatten-tmp')")).WillReturnResult(ok)
}

// expectFlattenQuietRun queues the rest of a quiet flatten: nothing staged
// after the reset, so no preservation commit, and a clean temp-branch delete.
func expectFlattenQuietRun(mock sqlmock.Sqlmock) {
	expectFlattenThroughReset(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestFlattenQuietRunSoftResetsMain pins the whole quiet sequence: main
// moves onto the flattened branch with DOLT_RESET('--soft', 'flatten-tmp'),
// never '--hard', and a clean working set adds no preservation commit.
func TestFlattenQuietRunSoftResetsMain(t *testing.T) {
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenQuietRun(mock)
	if err := Flatten(t.Context(), db); err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations: %v", merr)
	}
}

// TestFlattenPreservesStagedChanges: whatever the soft reset leaves staged on
// main — a commit that landed after the snapshot was built, or rows staged
// before the flatten — is committed by one preservation commit before the
// temp branch goes away.
func TestFlattenPreservesStagedChanges(t *testing.T) {
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenThroughReset(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(2))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs(flattenPreserveMsg).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := Flatten(t.Context(), db); err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations: %v", merr)
	}
}

// TestFlattenToleratesNothingToCommitPreservation: another writer committing
// main's staged set first leaves the preservation commit nothing to do,
// which is the outcome the step wants, not a failure.
func TestFlattenToleratesNothingToCommitPreservation(t *testing.T) {
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenThroughReset(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs(flattenPreserveMsg).
		WillReturnError(errors.New("Error 1105: nothing to commit"))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := Flatten(t.Context(), db); err != nil {
		t.Fatalf("Flatten: %v (a nothing-to-commit preservation must be benign)", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations: %v", merr)
	}
}
