package versioncontrolops

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// bd-7bpkd / ga-28co77: the failure paths of the hard-reset sites in this
// package, scripted with sqlmock's strict statement order. Every script
// models a store whose only clone-local table is events.

var errInjected = errors.New("injected failure")

// expectEventsProbe mocks one clone-local FK probe: events exists and
// fk_events_issue is (fkPresent) or is not on it; no other clone-local table
// exists.
func expectEventsProbe(mock sqlmock.Sqlmock, fkPresent bool) {
	for _, fk := range schema.CloneLocalFKs {
		exists := 0
		if fk.Table == "events" {
			exists = 1
		}
		mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?")).
			WithArgs(fk.Table).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(exists))
		if exists == 1 {
			n := 0
			if fkPresent {
				n = 1
			}
			mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLE_CONSTRAINTS")).
				WithArgs(fk.Table, fk.Constraint).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
		}
	}
}

// expectIssuesTableExists mocks the helper's check that fk_events_issue's
// referenced table survived the reset (made before re-linking it).
func expectIssuesTableExists(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?")).
		WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
}

// expectProbeFails mocks a pre-reset probe whose first query fails.
func expectProbeFails(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM information_schema.TABLES")).
		WithArgs("events").
		WillReturnError(errInjected)
}

// expectResetThenFailedRelink mocks a hard reset (to target, or HEAD when
// target is "") that drops fk_events_issue, whose re-link then fails at the
// ALTER.
func expectResetThenFailedRelink(mock sqlmock.Sqlmock, target string) {
	expectEventsProbe(mock, true)
	if target == "" {
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
	} else {
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard', ?)")).
			WithArgs(target).
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
	}
	expectEventsProbe(mock, false)
	expectIssuesTableExists(mock)
	mock.ExpectExec("DELETE FROM `?events`?").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ALTER TABLE `?events`? ADD CONSTRAINT `?fk_events_issue`?").WillReturnError(errInjected)
}

func expectExecOK(mock sqlmock.Sqlmock, query string, args ...driver.Value) {
	e := mock.ExpectExec(regexp.QuoteMeta(query))
	if len(args) > 0 {
		e = e.WithArgs(args...)
	}
	e.WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectFlattenUpToReset mocks Flatten from its log reads through the
// checkout of main that precedes the hard reset.
func expectFlattenUpToReset(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"commit_hash"}).AddRow("c0"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	expectExecOK(mock, "CALL DOLT_BRANCH('flatten-tmp')")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('flatten-tmp')")
	expectExecOK(mock, "CALL DOLT_RESET('--soft', ?)", "c0")
	expectExecOK(mock, "CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
}

func newMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, mock
}

func assertContainsAll(t *testing.T, what, msg string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Errorf("%s %q does not contain %q", what, msg, want)
		}
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// it wrote. The pipe is drained concurrently so a large write cannot block.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	drained := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		_ = r.Close()
		drained <- b.String()
	}()
	orig := os.Stderr
	os.Stderr = w
	func() {
		defer func() {
			os.Stderr = orig
			_ = w.Close()
		}()
		fn()
	}()
	return <-drained
}

// Item 1 (Astra blocker): a pre-reset probe failure must not reset main, but
// must still delete flatten-tmp — a leftover branch blocks every later
// flatten at "create temp branch".
func TestFlattenProbeFailureDeletesTempBranchWithoutResetting(t *testing.T) {
	db, mock := newMock(t)
	expectFlattenUpToReset(mock)
	expectProbeFails(mock)
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'flatten-tmp')")

	err := Flatten(context.Background(), db)
	if err == nil {
		t.Fatal("Flatten() error = nil, want the probe failure")
	}
	assertContainsAll(t, "Flatten() error", err.Error(), `flatten step "reset main to flattened"`, "reset not run", errInjected.Error())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (flatten-tmp left behind?): %v", err)
	}
}

// Item 3: a relink failure after a successful reset deletes flatten-tmp and
// returns the reset-succeeded error with the injected cause.
func TestFlattenRelinkFailureDeletesTempBranchAndReportsIt(t *testing.T) {
	db, mock := newMock(t)
	expectFlattenUpToReset(mock)
	expectResetThenFailedRelink(mock, "flatten-tmp")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'flatten-tmp')")

	err := Flatten(context.Background(), db)
	if err == nil {
		t.Fatal("Flatten() error = nil, want the relink failure")
	}
	assertContainsAll(t, "Flatten() error", err.Error(),
		`flatten step "reset main to flattened"`, "hard reset succeeded", "events.fk_events_issue", errInjected.Error())
	if !errors.Is(err, errInjected) {
		t.Errorf("Flatten() error does not wrap the injected ALTER failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Item 7 (nit): when deleting flatten-tmp also fails, the relink half keeps
// its step prefix and the cleanup failure is appended, on one line.
func TestFlattenRelinkFailureKeepsStepPrefixWhenCleanupFails(t *testing.T) {
	db, mock := newMock(t)
	expectFlattenUpToReset(mock)
	expectResetThenFailedRelink(mock, "flatten-tmp")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')")).
		WillReturnError(errors.New("injected branch delete failure"))

	err := Flatten(context.Background(), db)
	if err == nil {
		t.Fatal("Flatten() error = nil, want the relink failure")
	}
	msg := err.Error()
	assertContainsAll(t, "Flatten() error", msg,
		`flatten step "reset main to flattened": hard reset succeeded`, "events.fk_events_issue", "injected branch delete failure")
	if strings.Contains(msg, "\n") {
		t.Errorf("Flatten() error %q is multi-line", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Item 3: compact's deferred cleanup runs after a relink failure.
func TestCompactRelinkFailureRunsDeferredCleanup(t *testing.T) {
	db, mock := newMock(t)
	expectExecOK(mock, "CALL DOLT_BRANCH('compact-tmp', ?)", "c1")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('compact-tmp')")
	expectExecOK(mock, "CALL DOLT_RESET('--soft', ?)", "c0")
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectExecOK(mock, "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", "c2")
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectResetThenFailedRelink(mock, "compact-tmp")
	// Deferred cleanup.
	expectExecOK(mock, "CALL DOLT_CHECKOUT('main')")
	expectExecOK(mock, "CALL DOLT_BRANCH('-D', 'compact-tmp')")

	err := Compact(context.Background(), db, "c0", "c1", 2, []string{"c2"})
	if err == nil {
		t.Fatal("Compact() error = nil, want the relink failure")
	}
	assertContainsAll(t, "Compact() error", err.Error(),
		`compact step "reset main to compacted"`, "hard reset succeeded", "events.fk_events_issue", errInjected.Error())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Item 3: abortMerge warns on stderr when the reset succeeded but the relink
// failed.
func TestAbortMergeWarnsWhenRelinkFails(t *testing.T) {
	db, mock := newMock(t)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
		WillReturnError(errors.New("no merge to abort"))
	expectResetThenFailedRelink(mock, "")

	stderr := captureStderr(t, func() { abortMerge(context.Background(), db, true) })
	assertContainsAll(t, "abortMerge stderr", stderr,
		"Warning: merge abort recovery", "hard reset succeeded", "events.fk_events_issue", errInjected.Error())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Item 2 (Opus SF1): abortMerge is best-effort recovery; if the FK probe
// fails, it must still reset (bare) and say so on stderr.
func TestAbortMergeFallsBackToBareResetWhenProbeFails(t *testing.T) {
	db, mock := newMock(t)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
		WillReturnError(errors.New("no merge to abort"))
	expectProbeFails(mock)
	expectExecOK(mock, "CALL DOLT_RESET('--hard')")

	stderr := captureStderr(t, func() { abortMerge(context.Background(), db, true) })
	assertContainsAll(t, "abortMerge stderr", stderr, "Warning: merge abort recovery", errInjected.Error(), "bd doctor --fix")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (recovery reset skipped?): %v", err)
	}
}
