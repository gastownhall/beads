package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// compactVerifySQL compares the replay with the planned head (bound as its
// argument), not with main's current HEAD.
const compactVerifySQL = "SELECT COUNT(*) FROM dolt_diff_summary(?, 'compact-tmp')"

// expectCompactBeforeVerify queues Compact's steps up to and including the
// checkout of main that precedes the replay check: the replay of kept and
// the read-back of the rebuilt chain (compact_test.go).
func expectCompactBeforeVerify(mock sqlmock.Sqlmock, initial, boundary compactCommit, kept []compactCommit) {
	msg := expectCompactReplay(mock, initial, boundary, kept)
	mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
		WillReturnRows(compactLogRows(compactRebuiltLog(initial, boundary, kept, msg)))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectCompactCleanup queues the deferred cleanup of a failed Compact.
func expectCompactCleanup(mock sqlmock.Sqlmock) {
	ok := sqlmock.NewResult(0, 0)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
}

func runCompactForVerify(t *testing.T, setup func(sqlmock.Sqlmock)) error {
	t.Helper()
	db, mock := newMockDB(t)
	initial, boundary, kept := compactTestCommits()
	expectCompactBeforeVerify(mock, initial, boundary, kept)
	setup(mock)
	err := Compact(context.Background(), db, initial.Hash, boundary.Hash, "headhash", 2, keptHashes(kept))
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations (Compact returned %v): %v", err, merr)
	}
	return err
}

// TestCompactVerifyDivergentReplayAborts: a replay whose committed tables
// differ from main must not reset main; the temp branch is cleaned up.
func TestCompactVerifyDivergentReplayAborts(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		expectCompactCleanup(mock)
	})
	if err == nil || !strings.Contains(err.Error(), "replayed history differs from main's planned head headhash in 1 table(s); main was not reset") {
		t.Fatalf("Compact error = %v, want the divergence abort", err)
	}
}

func TestCompactVerifyQueryErrorAborts(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnError(errors.New("boom"))
		expectCompactCleanup(mock)
	})
	if err == nil || !strings.Contains(err.Error(), `compact step "verify replay"`) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Compact error = %v, want a wrapped verify replay error", err)
	}
}

func TestCompactVerifyScanErrorAborts(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow("not a number"))
		expectCompactCleanup(mock)
	})
	if err == nil || !strings.Contains(err.Error(), `compact step "verify replay"`) {
		t.Fatalf("Compact error = %v, want a wrapped verify replay error", err)
	}
}

// TestCompactVerifyEqualReplayResets: an empty diff lets Compact swap main and
// delete the temp branch.
func TestCompactVerifyEqualReplayResets(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		ok := sqlmock.NewResult(0, 0)
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'compact-tmp')")).WillReturnResult(ok)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
	})
	if err != nil {
		t.Fatalf("Compact error = %v, want nil", err)
	}
}

// TestCompactRequiresPlannedHead: without the planned head there is nothing
// to verify the replay against, so Compact refuses before it creates the
// temp branch or touches main.
func TestCompactRequiresPlannedHead(t *testing.T) {
	db, mock := newMockDB(t)
	err := Compact(context.Background(), db, "initial", "boundary", "", 3, []string{"recent"})
	if err == nil || !strings.Contains(err.Error(), "planned head") {
		t.Fatalf("Compact error = %v, want the missing planned head refused", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("Compact ran statements without a planned head: %v", merr)
	}
}
