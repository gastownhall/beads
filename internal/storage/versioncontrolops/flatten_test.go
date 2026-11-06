package versioncontrolops

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
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

// expectFlattenLeftoverCheck queues the read that refuses a leftover
// flatten-tmp before anything is created.
func expectFlattenLeftoverCheck(mock sqlmock.Sqlmock, existing int) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(existing))
}

// expectFlattenThroughReset queues the leftover check and the steps through
// the final soft reset of main onto flatten-tmp.
func expectFlattenThroughReset(mock sqlmock.Sqlmock) {
	expectFlattenLeftoverCheck(mock, 0)
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

// expectFlattenCleanup queues the deferred cleanup of a failed flatten: back
// to main, then the temp branch deleted.
func expectFlattenCleanup(mock sqlmock.Sqlmock) {
	ok := sqlmock.NewResult(0, 0)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).WillReturnResult(ok)
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

// TestFlattenStepFailureCleansUpTempBranch: any step that fails after the
// temp branch exists triggers the bounded cleanup — back to main, temp
// branch deleted — and the primary failure still surfaces. Each case queues
// the statements a successful run issues before the failing step.
func TestFlattenStepFailureCleansUpTempBranch(t *testing.T) {
	injected := errors.New("injected step failure")
	for _, tc := range []struct {
		name string
		fail func(mock sqlmock.Sqlmock)
	}{
		{"checkout temp branch", func(mock sqlmock.Sqlmock) {
			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('flatten-tmp')")).WillReturnError(injected)
		}},
		{"soft reset to initial", func(mock sqlmock.Sqlmock) {
			expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).
				WithArgs("inithash").WillReturnError(injected)
		}},
		{"commit flattened snapshot", func(mock sqlmock.Sqlmock) {
			expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
			expectExecOKArgs(mock, "CALL DOLT_RESET('--soft', ?)", "inithash")
			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")).
				WillReturnError(injected)
		}},
		{"checkout main", func(mock sqlmock.Sqlmock) {
			expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
			expectExecOKArgs(mock, "CALL DOLT_RESET('--soft', ?)", "inithash")
			expectExecOK(mock, "CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")
			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnError(injected)
		}},
		{"reset main to flattened", func(mock sqlmock.Sqlmock) {
			expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
			expectExecOKArgs(mock, "CALL DOLT_RESET('--soft', ?)", "inithash")
			expectExecOK(mock, "CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")
			expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'flatten-tmp')")).WillReturnError(injected)
		}},
		{"delete temp branch", func(mock sqlmock.Sqlmock) {
			expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
			expectExecOKArgs(mock, "CALL DOLT_RESET('--soft', ?)", "inithash")
			expectExecOK(mock, "CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")
			expectFlattenQuietRest(mock)
			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).WillReturnError(injected)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			expectFlattenReads(mock, 3)
			expectFlattenLeftoverCheck(mock, 0)
			expectExecOK(mock, "CALL DOLT_BRANCH('flatten-tmp')")
			tc.fail(mock)
			expectFlattenCleanup(mock)
			err := Flatten(t.Context(), db)
			if !errors.Is(err, injected) {
				t.Fatalf("Flatten error = %v, want it to wrap the injected failure", err)
			}
			if merr := mock.ExpectationsWereMet(); merr != nil {
				t.Errorf("unmet expectations: %v", merr)
			}
		})
	}
}

// expectExecOK queues one successful ExecContext of query.
func expectExecOK(mock sqlmock.Sqlmock, query string) {
	mock.ExpectExec(regexp.QuoteMeta(query)).WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectExecOKArgs queues one successful arg-bound ExecContext of query.
func expectExecOKArgs(mock sqlmock.Sqlmock, query string, args ...driver.Value) {
	mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectFlattenQuietRest queues the steps between the flattened snapshot
// commit and the temp-branch delete: the checkout of main, the final soft
// reset, and a quiet staged check.
func expectFlattenQuietRest(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'flatten-tmp')")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
}

// TestFlattenRefusesLeftoverBranch: a flatten-tmp this invocation did not
// create is refused up front, never deleted, and nothing else runs.
func TestFlattenRefusesLeftoverBranch(t *testing.T) {
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenLeftoverCheck(mock, 1)
	err := Flatten(t.Context(), db)
	if err == nil || !strings.Contains(err.Error(), "flatten refused") || !strings.Contains(err.Error(), "flatten-tmp") {
		t.Fatalf("Flatten error = %v, want the leftover flatten-tmp refused", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("Flatten ran statements despite the refusal: %v", merr)
	}
}

// TestFlattenCleanupFailureKeepsBranchAndJoins: when the cleanup's checkout
// fails too, the branch is kept (the delete must not run), and the returned
// error joins the primary and cleanup failures and names the branch.
func TestFlattenCleanupFailureKeepsBranchAndJoins(t *testing.T) {
	primary := errors.New("injected step failure")
	cleanupErr := errors.New("injected cleanup checkout failure")
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenLeftoverCheck(mock, 0)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('flatten-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('flatten-tmp')")).WillReturnError(primary)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnError(cleanupErr)
	err := Flatten(t.Context(), db)
	if !errors.Is(err, primary) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Flatten error = %v, want the primary and cleanup failures joined", err)
	}
	if !strings.Contains(err.Error(), "flatten-tmp") {
		t.Fatalf("Flatten error = %v, want it to name the branch left behind", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations: %v", merr)
	}
}

// AC-5: once the final reset has moved main onto the flattened history, a
// failed staged check or preservation commit reports the partial outcome and
// the recovery instead of rolling anything back: main keeps the flattened
// history, and its staged set waits for a manual `bd dolt commit`.
func TestFlattenStagedCheckFailureReportsPartialOutcome(t *testing.T) {
	injected := errors.New("injected staged-check failure")
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenThroughReset(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnError(injected)
	expectFlattenCleanup(mock)
	err := Flatten(t.Context(), db)
	if !errors.Is(err, injected) {
		t.Fatalf("Flatten error = %v, want it to wrap the injected failure", err)
	}
	assertFlattenPartialOutcome(t, err, `flatten step "check main for staged changes" failed`)
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations: %v", merr)
	}
}

// TestFlattenPreservationFailureReportsPartialOutcome: the same partial
// outcome when the preservation commit itself fails.
func TestFlattenPreservationFailureReportsPartialOutcome(t *testing.T) {
	injected := errors.New("injected preservation failure")
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenThroughReset(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs(flattenPreserveMsg).WillReturnError(injected)
	expectFlattenCleanup(mock)
	err := Flatten(t.Context(), db)
	if !errors.Is(err, injected) {
		t.Fatalf("Flatten error = %v, want it to wrap the injected failure", err)
	}
	assertFlattenPartialOutcome(t, err, `flatten step "commit changes that reached main" failed`)
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations: %v", merr)
	}
}

// assertFlattenPartialOutcome checks the error names the failed step, the
// partial outcome (main already on the flattened history) and the recovery.
func assertFlattenPartialOutcome(t *testing.T, err error, wantStep string) {
	t.Helper()
	for _, want := range []string{
		wantStep,
		"main already points at the flattened history",
		"dolt_status",
		"bd dolt commit",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Flatten error = %v, want it to contain %q", err, want)
		}
	}
}

// TestFlattenAlreadyFlatIsNoOp: a one-commit store has nothing to squash;
// Flatten returns nil without creating a branch or touching anything.
func TestFlattenAlreadyFlatIsNoOp(t *testing.T) {
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 1)
	if err := Flatten(t.Context(), db); err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations (a no-op flatten must issue nothing): %v", merr)
	}
}

// TestFlattenCleanupRunsOnLiveDetachedContext: the cleanup must run even
// when the primary failure is the caller's cancellation, so its statements
// run on a context that is not the cancelled one. The cancelled delete never
// reaches the driver; both cleanup statements do.
func TestFlattenCleanupRunsOnLiveDetachedContext(t *testing.T) {
	db, mock := newMockDB(t)
	expectFlattenReads(mock, 3)
	expectFlattenLeftoverCheck(mock, 0)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('flatten-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('flatten-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).
		WithArgs("inithash").WillReturnError(context.Canceled)
	expectFlattenCleanup(mock)
	err := Flatten(t.Context(), db)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Flatten error = %v, want it to preserve the cancellation", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations (the cleanup must run on a detached context): %v", merr)
	}
}
