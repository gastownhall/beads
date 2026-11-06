package versioncontrolops

import (
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

// expectFlattenQuietRun queues a full quiet flatten: the temp branch, the
// snapshot on it, and main soft-reset onto it with nothing staged, so no
// preservation commit and a clean temp-branch delete.
func expectFlattenQuietRun(mock sqlmock.Sqlmock) {
	ok := sqlmock.NewResult(0, 1)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('flatten-tmp')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('flatten-tmp')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).
		WithArgs("inithash").WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")).
		WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'flatten-tmp')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).WillReturnResult(ok)
}

// TestFlattenQuietRunSoftResetsMain pins the whole quiet sequence: main
// moves onto the flattened branch with DOLT_RESET('--soft', 'flatten-tmp'),
// never '--hard'.
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
