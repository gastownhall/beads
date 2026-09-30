package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// bd-7bpkd / ga-28co77, review round 3 (Astra r2): cleanup under
// cancellation, cleanup failures that must be reported, failures before the
// reset, and abortMerge's reset count and failed recovery.

// recordingConn wraps the mock session. It counts hard resets and keeps every
// error the mock returned, so a statement production code issues but ignores
// the error of (for example a second, unexpected reset) still fails the test:
// sqlmock's ExpectationsWereMet only reports expectations never met, not
// unexpected calls. afterExec, if set, runs after each successful Exec.
type recordingConn struct {
	inner     DBConn
	resets    int
	errs      []error
	afterExec func(query string)
}

func (r *recordingConn) note(query string, err error) {
	if strings.Contains(query, "DOLT_RESET('--hard'") {
		r.resets++
	}
	if err != nil {
		r.errs = append(r.errs, err)
	}
}

func (r *recordingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := r.inner.ExecContext(ctx, query, args...)
	r.note(query, err)
	if err == nil && r.afterExec != nil {
		r.afterExec(query)
	}
	return res, err
}

func (r *recordingConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := r.inner.QueryContext(ctx, query, args...)
	r.note(query, err)
	return rows, err
}

func (r *recordingConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	r.note(query, nil)
	return r.inner.QueryRowContext(ctx, query, args...)
}

// assertNoUnexpectedCalls fails if the mock rejected any statement as
// unexpected or out of order.
func (r *recordingConn) assertNoUnexpectedCalls(t *testing.T) {
	t.Helper()
	for _, err := range r.errs {
		msg := err.Error()
		if strings.Contains(msg, "was not expected") || strings.Contains(msg, "could not match actual sql") {
			t.Errorf("production issued an unexpected statement (its error may have been ignored): %v", err)
		}
	}
}

// --- Flatten / Compact cleanup ------------------------------------------------

// BLOCKER (Astra r2, flatten.go:53): the caller's context is canceled right
// after the session checks out flatten-tmp. The next step fails with the
// cancellation, and the cleanup must STILL check out main and delete the
// branch — on a context that does not share the caller's cancellation.
func TestFlattenCleanupRunsAfterCallerCancellation(t *testing.T) {
	db, mock := newMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := &recordingConn{inner: db, afterExec: func(q string) {
		if strings.Contains(q, "DOLT_CHECKOUT('flatten-tmp')") {
			cancel()
		}
	}}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"commit_hash"}).AddRow("c0"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	expectExecOK(mock, "CALL DOLT_BRANCH('flatten-tmp')")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
	// The soft reset never reaches the server: the context is already done.
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'flatten-tmp')")

	err := Flatten(ctx, rc)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Flatten() error = %v, want the caller's cancellation", err)
	}
	if strings.Contains(err.Error(), "cleanup also failed") {
		t.Errorf("Flatten() error %q reports a cleanup failure, want cleanup to succeed despite the cancellation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("cleanup did not run after cancellation (session left on flatten-tmp, branch stranded): %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// A failed checkout of main during cleanup must be reported, not discarded:
// the session is still on flatten-tmp.
func TestFlattenReportsCleanupCheckoutFailure(t *testing.T) {
	db, mock := newMock(t)
	expectFlattenUpToReset(mock)
	expectProbeFails(mock)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).
		WillReturnError(errors.New("injected checkout failure"))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).
		WillReturnError(errors.New("injected branch delete failure"))

	err := Flatten(context.Background(), db)
	if err == nil {
		t.Fatal("Flatten() error = nil, want the probe failure")
	}
	assertContainsAll(t, "Flatten() error", err.Error(),
		`flatten step "reset main to flattened"`, "cleanup also failed",
		"checkout main", "injected checkout failure",
		"delete temp branch flatten-tmp", "injected branch delete failure")
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("Flatten() error %q is multi-line", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A step failing before the reset (after flatten-tmp exists) cleans up and
// never probes or resets.
func TestFlattenStepFailureBeforeResetCleansUp(t *testing.T) {
	db, mock := newMock(t)
	rc := &recordingConn{inner: db}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"commit_hash"}).AddRow("c0"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	expectExecOK(mock, "CALL DOLT_BRANCH('flatten-tmp')")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
	expectExecOK(mock, "CALL DOLT_RESET('--soft', ?)", "c0")
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")).
		WillReturnError(errInjected)
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'flatten-tmp')")

	err := Flatten(context.Background(), rc)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Flatten() error = %v, want the commit failure", err)
	}
	if rc.resets != 0 {
		t.Errorf("hard resets = %d, want 0 (the failure came before the reset)", rc.resets)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// If creating flatten-tmp itself fails there is nothing to clean up: no
// checkout, no delete (the branch may belong to someone else).
func TestFlattenCreateBranchFailureSkipsCleanup(t *testing.T) {
	db, mock := newMock(t)
	rc := &recordingConn{inner: db}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"commit_hash"}).AddRow("c0"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('flatten-tmp')")).WillReturnError(errInjected)

	if err := Flatten(context.Background(), rc); !errors.Is(err, errInjected) {
		t.Fatalf("Flatten() error = %v, want the branch-create failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// Compact has the same cleanup: it too must run after the caller's context is
// canceled.
func TestCompactCleanupRunsAfterCallerCancellation(t *testing.T) {
	db, mock := newMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := &recordingConn{inner: db, afterExec: func(q string) {
		if strings.Contains(q, "DOLT_CHECKOUT('compact-tmp')") {
			cancel()
		}
	}}
	expectExecOK(mock, "CALL DOLT_BRANCH('compact-tmp', ?)", "c1")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('compact-tmp')")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'compact-tmp')")

	err := Compact(ctx, rc, "c0", "c1", 2, []string{"c2"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact() error = %v, want the caller's cancellation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("cleanup did not run after cancellation (compact-tmp stranded): %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// --- abortMerge ----------------------------------------------------------------

// The bare fallback reset's failure must be reported as a failed recovery.
func TestAbortMergeReportsFailedBareReset(t *testing.T) {
	db, mock := newMock(t)
	rc := &recordingConn{inner: db}
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
		WillReturnError(errors.New("no merge to abort"))
	expectProbeFails(mock)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnError(errors.New("injected bare reset failure"))

	stderr := captureStderr(t, func() { abortMerge(context.Background(), rc, true) })
	assertContainsAll(t, "abortMerge stderr", stderr, "merge abort recovery failed", "injected bare reset failure")
	if rc.resets != 1 {
		t.Errorf("hard resets = %d, want exactly 1 (the bare fallback)", rc.resets)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// The helper's own reset failing is a failed recovery too, and must not be
// retried with a second reset.
func TestAbortMergeReportsFailedResetWithoutRetrying(t *testing.T) {
	db, mock := newMock(t)
	rc := &recordingConn{inner: db}
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
		WillReturnError(errors.New("no merge to abort"))
	expectEventsProbe(mock, true)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnError(errors.New("injected helper reset failure"))

	stderr := captureStderr(t, func() { abortMerge(context.Background(), rc, true) })
	assertContainsAll(t, "abortMerge stderr", stderr, "merge abort recovery failed", "injected helper reset failure")
	if rc.resets != 1 {
		t.Errorf("hard resets = %d, want exactly 1", rc.resets)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// Reset counts on the existing paths: a relink failure after a successful
// reset must not trigger the bare fallback (one reset, not two); a probe
// failure triggers exactly one (bare) reset.
func TestAbortMergeResetCounts(t *testing.T) {
	t.Run("relink failure: one reset", func(t *testing.T) {
		db, mock := newMock(t)
		rc := &recordingConn{inner: db}
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
			WillReturnError(errors.New("no merge to abort"))
		expectResetThenFailedRelink(mock, "")
		_ = captureStderr(t, func() { abortMerge(context.Background(), rc, true) })
		if rc.resets != 1 {
			t.Errorf("hard resets = %d, want exactly 1", rc.resets)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		rc.assertNoUnexpectedCalls(t)
	})
	t.Run("probe failure: one bare reset", func(t *testing.T) {
		db, mock := newMock(t)
		rc := &recordingConn{inner: db}
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
			WillReturnError(errors.New("no merge to abort"))
		expectProbeFails(mock)
		expectExecOK(mock, "CALL DOLT_RESET('--hard')")
		_ = captureStderr(t, func() { abortMerge(context.Background(), rc, true) })
		if rc.resets != 1 {
			t.Errorf("hard resets = %d, want exactly 1", rc.resets)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		rc.assertNoUnexpectedCalls(t)
	})
	t.Run("dirty pre-merge working set: no reset", func(t *testing.T) {
		db, mock := newMock(t)
		rc := &recordingConn{inner: db}
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
			WillReturnError(errors.New("no merge to abort"))
		_ = captureStderr(t, func() { abortMerge(context.Background(), rc, false) })
		if rc.resets != 0 {
			t.Errorf("hard resets = %d, want 0 (bd-578h9.2: never reset over pre-existing work)", rc.resets)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		rc.assertNoUnexpectedCalls(t)
	})
}
