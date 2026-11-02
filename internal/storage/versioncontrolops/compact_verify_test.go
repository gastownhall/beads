package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

var errDiffSummary = errors.New("diff summary exploded")

// These tests use sqlmock, matching the convention already established in
// this package (see fastforward_test.go, clone_test.go): the replay
// verification added for #6645 is a guarded SQL sequence, and sqlmock can
// prove the divergence/query/scan failures exclude the hard reset and reach
// the deferred cleanup without a live Dolt history. The behavioral end-to-end
// case against a real divergent replay is TestEmbeddedCompactAbortsDivergentReplay
// (cmd/bd).

// expectCompactReplayToMain programs the mock for a Compact run that gets all
// the way through the replay and the checkout of main: temp branch, checkout,
// soft reset, squashed-base commit, one cherry-pick, checkout main.
func expectCompactReplayToMain(t *testing.T, mock sqlmock.Sqlmock, initial, boundary, recent string) {
	t.Helper()
	ok := sqlmock.NewResult(0, 1)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('compact-tmp', ?)")).
		WithArgs(boundary).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('compact-tmp')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).
		WithArgs(initial).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?)")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHERRY_PICK('--allow-empty', ?)")).
		WithArgs(recent).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(ok)
}

// expectCompactCleanup programs the deferred best-effort cleanup: back to
// main, temp branch deleted.
func expectCompactCleanup(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	ok := sqlmock.NewResult(0, 1)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
}

const verifyReplayQuery = "SELECT COUNT(*) FROM dolt_diff_summary('main', 'compact-tmp')"

// TestCompactVerifyAbortsOnDivergentReplay covers #6645: a replay whose
// committed content differs from main must abort before the hard reset — the
// error names the divergence, and the expectations being met proves the reset
// never ran while the deferred cleanup did.
func TestCompactVerifyAbortsOnDivergentReplay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCompactReplayToMain(t, mock, "initial0", "boundary0", "recent00")
	mock.ExpectQuery(regexp.QuoteMeta(verifyReplayQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	expectCompactCleanup(t, mock)

	err = Compact(context.Background(), db, "initial0", "boundary0", 5, []string{"recent00"})
	if err == nil {
		t.Fatal("Compact: expected divergence error, got nil")
	}
	for _, want := range []string{
		"compact aborted: replayed history differs from main in 2 table(s)",
		"main was not reset",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactVerifyQueryError fails the verification query itself: the error
// is wrapped as the verify step, the reset is excluded, and cleanup runs.
func TestCompactVerifyQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCompactReplayToMain(t, mock, "initial0", "boundary0", "recent00")
	mock.ExpectQuery(regexp.QuoteMeta(verifyReplayQuery)).
		WillReturnError(errDiffSummary)
	expectCompactCleanup(t, mock)

	err = Compact(context.Background(), db, "initial0", "boundary0", 5, []string{"recent00"})
	if err == nil {
		t.Fatal("Compact: expected verify error, got nil")
	}
	if !strings.Contains(err.Error(), `compact step "verify replay"`) || !strings.Contains(err.Error(), errDiffSummary.Error()) {
		t.Errorf("error = %v, want compact step %q wrapping %v", err, "verify replay", errDiffSummary)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactVerifyScanError fails the row scan: same handling as a query
// error — wrapped, reset excluded, cleanup runs.
func TestCompactVerifyScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCompactReplayToMain(t, mock, "initial0", "boundary0", "recent00")
	// A count that is not an integer makes the Scan into int fail.
	mock.ExpectQuery(regexp.QuoteMeta(verifyReplayQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow("not-a-count"))
	expectCompactCleanup(t, mock)

	err = Compact(context.Background(), db, "initial0", "boundary0", 5, []string{"recent00"})
	if err == nil {
		t.Fatal("Compact: expected scan error, got nil")
	}
	if !strings.Contains(err.Error(), `compact step "verify replay"`) {
		t.Errorf("error = %v, want compact step %q", err, "verify replay")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactVerifyCleanReplayResets: a zero-row diff summary lets the swap
// proceed — the hard reset and temp-branch deletion run and Compact succeeds.
func TestCompactVerifyCleanReplayResets(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	ok := sqlmock.NewResult(0, 1)
	expectCompactReplayToMain(t, mock, "initial0", "boundary0", "recent00")
	mock.ExpectQuery(regexp.QuoteMeta(verifyReplayQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--hard', 'compact-tmp')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)

	if err := Compact(context.Background(), db, "initial0", "boundary0", 5, []string{"recent00"}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
