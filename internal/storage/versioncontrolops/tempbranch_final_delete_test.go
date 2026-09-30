package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// bd-7bpkd / ga-28co77, review round 4 (Astra r3 on cb5d1e7a9): the FINAL
// delete of the temp branch, after the reset and relink have succeeded, must
// also go through the cancellation-safe cleanup when it fails; otherwise the
// temp branch is left behind and blocks every later run.

// expectSuccessfulResetAndRelink mocks a hard reset to target that drops
// fk_events_issue and a relink that restores it.
func expectSuccessfulResetAndRelink(mock sqlmock.Sqlmock, target string) {
	expectEventsProbe(mock, true)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard', ?)")).
		WithArgs(target).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectEventsProbe(mock, false)
	expectIssuesTableExists(mock)
	mock.ExpectExec("DELETE FROM `?events`?").WillReturnResult(sqlmock.NewResult(0, 0))
	expectExecOK(mock, "ALTER TABLE `events` ADD CONSTRAINT `fk_events_issue` FOREIGN KEY (`issue_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE")
}

// cancelAfterRelink cancels ctx right after the relink's ADD CONSTRAINT,
// i.e. after the flatten/compact itself has succeeded.
func cancelAfterRelink(cancel context.CancelFunc) func(string) {
	return func(q string) {
		if strings.Contains(q, "ADD CONSTRAINT") {
			cancel()
		}
	}
}

// The caller is canceled after the flatten succeeded: the final delete fails
// on the canceled context, and the deferred cleanup must still check out main
// and delete flatten-tmp on its own context. Once that retry has deleted the
// branch the flatten has fully succeeded, so Flatten returns nil (upstream
// review 1, MINOR 3) and reports the retried delete as a stderr warning, the
// way this package reports other non-fatal events.
func TestFlattenFinalDeleteRetriedByCleanupReturnsNilWithWarning(t *testing.T) {
	db, mock := newMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := &recordingConn{inner: db, afterExec: cancelAfterRelink(cancel)}
	expectFlattenUpToReset(mock)
	expectSuccessfulResetAndRelink(mock, "flatten-tmp")
	// The final delete never reaches the server (ctx is canceled); the
	// deferred cleanup does.
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'flatten-tmp')")

	var err error
	stderr := captureStderr(t, func() { err = Flatten(ctx, rc) })
	if err != nil {
		t.Fatalf("Flatten() error = %v, want nil: the flatten succeeded and the cleanup retry deleted flatten-tmp", err)
	}
	assertContainsAll(t, "Flatten stderr", stderr,
		"Warning:", "flatten succeeded", "flatten-tmp", "context canceled", "cleanup retry deleted")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("flatten-tmp was not cleaned up after the final delete failed: %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// Same, with the final delete failing on an injected server error, and the
// cleanup's retry failing too: both failures are reported, and the error
// still says the flatten itself succeeded.
func TestFlattenFinalDeleteFailureReportsCleanupToo(t *testing.T) {
	db, mock := newMock(t)
	rc := &recordingConn{inner: db}
	expectFlattenUpToReset(mock)
	expectSuccessfulResetAndRelink(mock, "flatten-tmp")
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).
		WillReturnError(errors.New("injected final delete failure"))
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).
		WillReturnError(errors.New("injected cleanup delete failure"))

	err := Flatten(context.Background(), rc)
	if err == nil {
		t.Fatal("Flatten() error = nil, want the failed delete")
	}
	assertContainsAll(t, "Flatten() error", err.Error(),
		"flatten succeeded", "injected final delete failure",
		"cleanup also failed", "injected cleanup delete failure")
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("Flatten() error %q is multi-line", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the deferred cleanup did not retry the delete: %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}

// Regression guard: Compact already keeps its flag set through the final
// delete, so the same cancellation goes through its deferred cleanup.
func TestCompactFinalDeleteAfterCancellationGoesThroughCleanup(t *testing.T) {
	db, mock := newMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := &recordingConn{inner: db, afterExec: cancelAfterRelink(cancel)}
	expectExecOK(mock, "CALL DOLT_BRANCH('compact-tmp', ?)", "c1")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('compact-tmp')")
	expectExecOK(mock, "CALL DOLT_RESET('--soft', ?)", "c0")
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectExecOK(mock, "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", "c2")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectSuccessfulResetAndRelink(mock, "compact-tmp")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'compact-tmp')")

	err := Compact(ctx, rc, "c0", "c1", 2, []string{"c2"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact() error = %v, want the canceled final delete", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("compact-tmp was not cleaned up after the final delete failed: %v", err)
	}
	rc.assertNoUnexpectedCalls(t)
}
