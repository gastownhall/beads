package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// flattenStep is one statement Flatten issues, in order, as sqlmock sees it.
type flattenStep struct {
	name  string
	query string
	args  []any
	// kind is "exec", "row" (QueryRowContext) or "query" (QueryContext).
	kind string
	rows func() *sqlmock.Rows
}

const flattenInitialHash = "initial0hash"

// flattenSteps lists Flatten's statements on a three-commit history. With
// staged set, main holds staged rows after the final reset, so Flatten adds
// the preservation commit.
func flattenSteps(staged bool) []flattenStep {
	stagedCount := 0
	if staged {
		stagedCount = 1
	}
	steps := []flattenStep{
		{name: "find initial commit", kind: "row", query: "SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1",
			rows: func() *sqlmock.Rows { return sqlmock.NewRows([]string{"commit_hash"}).AddRow(flattenInitialHash) }},
		{name: "count commits", kind: "row", query: "SELECT COUNT(*) FROM dolt_log",
			rows: func() *sqlmock.Rows { return sqlmock.NewRows([]string{"count"}).AddRow(3) }},
		{name: "create temp branch", kind: "exec", query: "CALL DOLT_BRANCH('flatten-tmp')"},
		{name: "checkout temp branch", kind: "exec", query: "CALL DOLT_CHECKOUT('flatten-tmp')"},
		{name: "soft reset to initial", kind: "exec", query: "CALL DOLT_RESET('--soft', ?)", args: []any{flattenInitialHash}},
		{name: "commit flattened snapshot", kind: "exec", query: "CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')"},
		{name: "checkout main", kind: "exec", query: "CALL DOLT_CHECKOUT('main')"},
		{name: "reset main to flattened", kind: "exec", query: "CALL DOLT_RESET('--soft', 'flatten-tmp')"},
		{name: "check main for staged changes", kind: "row", query: "SELECT COUNT(*) FROM dolt_status WHERE staged = 1",
			rows: func() *sqlmock.Rows { return sqlmock.NewRows([]string{"count"}).AddRow(stagedCount) }},
	}
	if staged {
		steps = append(steps, flattenStep{name: "commit changes that reached main", kind: "query",
			query: "CALL DOLT_COMMIT('-m', ?)", args: []any{"flatten: commit changes that reached main during flatten"},
			rows: func() *sqlmock.Rows { return sqlmock.NewRows([]string{"hash"}).AddRow("followup0hash") }})
	}
	return append(steps, flattenStep{name: "delete temp branch", kind: "exec", query: "CALL DOLT_BRANCH('-D', 'flatten-tmp')"})
}

// expectFlattenThrough programs steps up to and including the one named
// failAt, which fails with failErr; an empty failAt programs every step.
func expectFlattenThrough(mock sqlmock.Sqlmock, steps []flattenStep, failAt string, failErr error) {
	for _, s := range steps {
		fail := s.name == failAt
		args := make([]driver.Value, 0, len(s.args))
		for _, a := range s.args {
			args = append(args, a)
		}
		switch s.kind {
		case "exec":
			e := mock.ExpectExec(regexp.QuoteMeta(s.query)).WithArgs(args...)
			if fail {
				e.WillReturnError(failErr)
			} else {
				e.WillReturnResult(sqlmock.NewResult(0, 1))
			}
		default:
			e := mock.ExpectQuery(regexp.QuoteMeta(s.query)).WithArgs(args...)
			if fail {
				e.WillReturnError(failErr)
			} else {
				e.WillReturnRows(s.rows())
			}
		}
		if fail {
			return
		}
	}
}

// expectFlattenCleanup programs the cleanup's return to main and branch
// delete; a non-nil error fails that statement.
func expectFlattenCleanup(mock sqlmock.Sqlmock, checkoutErr, deleteErr error) {
	checkout := mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')"))
	if checkoutErr != nil {
		checkout.WillReturnError(checkoutErr)
		return
	}
	checkout.WillReturnResult(sqlmock.NewResult(0, 1))
	del := mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'flatten-tmp')"))
	if deleteErr != nil {
		del.WillReturnError(deleteErr)
		return
	}
	del.WillReturnResult(sqlmock.NewResult(0, 1))
}

// ctxRecordingConn records the context of every ExecContext Flatten issues,
// optionally acting just before the statement is forwarded.
type ctxRecordingConn struct {
	DBConn
	beforeExec func(query string)
	execs      []recordedExec
}

type recordedExec struct {
	query       string
	ctxErr      error
	deadline    time.Time
	hasDeadline bool
	at          time.Time
}

func (c *ctxRecordingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if c.beforeExec != nil {
		c.beforeExec(query)
	}
	deadline, ok := ctx.Deadline()
	c.execs = append(c.execs, recordedExec{query: query, ctxErr: ctx.Err(), deadline: deadline, hasDeadline: ok, at: time.Now()})
	return c.DBConn.ExecContext(ctx, query, args...)
}

// wantCleanupContext checks the last n statements ran on a live context
// bounded by at most 30 seconds.
func (c *ctxRecordingConn) wantCleanupContext(t *testing.T, n int) {
	t.Helper()
	if len(c.execs) < n {
		t.Fatalf("recorded %d statements, want at least %d cleanup statements", len(c.execs), n)
	}
	for _, e := range c.execs[len(c.execs)-n:] {
		if e.ctxErr != nil {
			t.Errorf("cleanup %q ran on a done context: %v", e.query, e.ctxErr)
		}
		if !e.hasDeadline {
			t.Errorf("cleanup %q ran without a deadline, want one bounded at 30s", e.query)
			continue
		}
		if left := e.deadline.Sub(e.at); left <= 0 || left > 30*time.Second {
			t.Errorf("cleanup %q deadline %s away, want within 30s", e.query, left)
		}
	}
}

func newFlattenMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// AC-4: a failure at any step after Flatten creates flatten-tmp returns to
// main and deletes the branch on a live context bounded at 30 seconds, and
// returns the step's error.
func TestFlattenCleanupAfterEachStepFails(t *testing.T) {
	for _, staged := range []bool{false, true} {
		steps := flattenSteps(staged)
		prefix := "clean main/"
		if staged {
			prefix = "staged main/"
		}
		created := false
		for _, s := range steps {
			if s.name == "create temp branch" {
				created = true
				continue
			}
			if !created {
				continue
			}
			t.Run(prefix+s.name, func(t *testing.T) {
				db, mock := newFlattenMock(t)
				injected := errors.New("injected " + s.name + " failure")
				expectFlattenThrough(mock, steps, s.name, injected)
				expectFlattenCleanup(mock, nil, nil)
				conn := &ctxRecordingConn{DBConn: db}

				err := Flatten(context.Background(), conn)
				if !errors.Is(err, injected) {
					t.Fatalf("Flatten error = %v, want the injected %s failure", err, s.name)
				}
				if !strings.Contains(err.Error(), s.name) {
					t.Errorf("Flatten error %q does not name the failed step %q", err, s.name)
				}
				conn.wantCleanupContext(t, 2)
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("cleanup statements: %v", err)
				}
			})
		}
	}
}

// AC-4: a caller cancelling at the final branch delete still gets flatten-tmp
// removed: the cleanup runs on a context the cancellation does not reach, and
// the error still reports the cancellation.
func TestFlattenCleanupSurvivesCancellationAtFinalDelete(t *testing.T) {
	db, mock := newFlattenMock(t)
	steps := flattenSteps(false)
	// The canceled delete never reaches the driver: database/sql refuses a
	// done context first.
	expectFlattenThrough(mock, steps[:len(steps)-1], "", nil)
	expectFlattenCleanup(mock, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := false
	conn := &ctxRecordingConn{DBConn: db, beforeExec: func(query string) {
		if !canceled && strings.Contains(query, "DOLT_BRANCH('-D'") {
			canceled = true
			cancel()
		}
	}}

	err := Flatten(ctx, conn)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Flatten error = %v, want it to report the cancellation", err)
	}
	conn.wantCleanupContext(t, 2)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("cleanup statements: %v", err)
	}
}

// AC-4: a cleanup that fails joins its error to the primary one and names the
// branch left behind; one that cannot return to main keeps the branch rather
// than deleting it from under the session.
func TestFlattenCleanupFailureIsReported(t *testing.T) {
	primary := errors.New("injected snapshot commit failure")

	t.Run("delete fails", func(t *testing.T) {
		db, mock := newFlattenMock(t)
		cleanupErr := errors.New("injected cleanup delete failure")
		expectFlattenThrough(mock, flattenSteps(false), "commit flattened snapshot", primary)
		expectFlattenCleanup(mock, nil, cleanupErr)

		err := Flatten(context.Background(), &ctxRecordingConn{DBConn: db})
		if !errors.Is(err, primary) || !errors.Is(err, cleanupErr) {
			t.Fatalf("Flatten error = %v, want both the primary and the cleanup failure", err)
		}
		if !strings.Contains(err.Error(), "flatten-tmp") {
			t.Errorf("Flatten error %q does not name the remaining branch flatten-tmp", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("cleanup statements: %v", err)
		}
	})

	t.Run("checkout main fails", func(t *testing.T) {
		db, mock := newFlattenMock(t)
		checkoutErr := errors.New("injected cleanup checkout failure")
		expectFlattenThrough(mock, flattenSteps(false), "commit flattened snapshot", primary)
		expectFlattenCleanup(mock, checkoutErr, nil)

		err := Flatten(context.Background(), &ctxRecordingConn{DBConn: db})
		if !errors.Is(err, primary) || !errors.Is(err, checkoutErr) {
			t.Fatalf("Flatten error = %v, want both the primary and the checkout failure", err)
		}
		if !strings.Contains(err.Error(), "flatten-tmp") || !strings.Contains(err.Error(), "kept") {
			t.Errorf("Flatten error %q does not say flatten-tmp was kept", err)
		}
		// sqlmock fails any statement it was not told to expect, so a
		// branch delete after the failed checkout surfaces here.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("cleanup statements: %v", err)
		}
	})
}

// AC-4: a flatten-tmp that existed before Flatten ran is not this
// invocation's to remove: creating the branch fails and nothing is cleaned up.
// The error says how to delete a branch an unfinished flatten left.
func TestFlattenLeavesPreexistingTempBranch(t *testing.T) {
	db, mock := newFlattenMock(t)
	exists := errors.New("fatal: A branch named 'flatten-tmp' already exists.")
	expectFlattenThrough(mock, flattenSteps(false), "create temp branch", exists)

	conn := &ctxRecordingConn{DBConn: db}
	err := Flatten(context.Background(), conn)
	if !errors.Is(err, exists) {
		t.Fatalf("Flatten error = %v, want the branch-exists failure", err)
	}
	if !strings.Contains(err.Error(), "CALL DOLT_BRANCH('-D', 'flatten-tmp')") {
		t.Errorf("Flatten error %q does not say how to delete the leftover flatten-tmp", err)
	}
	if len(conn.execs) != 1 {
		t.Fatalf("Flatten issued %d statements, want only the failed branch create", len(conn.execs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("statements: %v", err)
	}
}

// A branch create that fails for any other reason gets no advice to delete
// flatten-tmp.
func TestFlattenOtherCreateFailureHasNoDeleteAdvice(t *testing.T) {
	db, mock := newFlattenMock(t)
	injected := errors.New("injected create temp branch failure")
	expectFlattenThrough(mock, flattenSteps(false), "create temp branch", injected)

	err := Flatten(context.Background(), &ctxRecordingConn{DBConn: db})
	if !errors.Is(err, injected) {
		t.Fatalf("Flatten error = %v, want the injected create failure", err)
	}
	if strings.Contains(err.Error(), "DOLT_BRANCH('-D'") {
		t.Errorf("Flatten error %q advises deleting a flatten-tmp that does not exist", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("statements: %v", err)
	}
}

// AC-5: once main points at the flattened history, a failure checking or
// committing its staged set names the failed step, says main already moved
// and how to commit what it kept, and never hard-resets main back.
func TestFlattenAfterHeadMoveErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		staged bool
		failAt string
	}{
		{name: "staged check fails", failAt: "check main for staged changes"},
		{name: "preservation commit fails", staged: true, failAt: "commit changes that reached main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newFlattenMock(t)
			injected := errors.New("injected " + tc.name)
			expectFlattenThrough(mock, flattenSteps(tc.staged), tc.failAt, injected)
			expectFlattenCleanup(mock, nil, nil)
			conn := &ctxRecordingConn{DBConn: db}

			err := Flatten(context.Background(), conn)
			if !errors.Is(err, injected) {
				t.Fatalf("Flatten error = %v, want it to wrap %v", err, injected)
			}
			for _, want := range []string{tc.failAt, "main already points at the flattened history", "dolt_status", "bd dolt commit"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Flatten error = %q, want it to mention %q", err, want)
				}
			}
			for _, e := range conn.execs {
				if strings.Contains(e.query, "--hard") {
					t.Errorf("Flatten issued %q after main moved, want no hard reset", e.query)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("statements: %v", err)
			}
		})
	}

	// The step's benign outcomes are not partial: nothing staged commits
	// nothing, and a writer committing the staged set first is success.
	t.Run("nothing staged", func(t *testing.T) {
		db, mock := newFlattenMock(t)
		expectFlattenThrough(mock, flattenSteps(false), "", nil)
		if err := Flatten(context.Background(), db); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("statements: %v", err)
		}
	})

	t.Run("nothing to commit", func(t *testing.T) {
		db, mock := newFlattenMock(t)
		steps := flattenSteps(true)
		expectFlattenThrough(mock, steps[:len(steps)-2], "", nil)
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
			WillReturnError(errors.New("nothing to commit"))
		expectFlattenThrough(mock, steps[len(steps)-1:], "", nil)
		if err := Flatten(context.Background(), db); err != nil {
			t.Fatalf("Flatten: %v (another writer committing first is success)", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("statements: %v", err)
		}
	})

	t.Run("already flat", func(t *testing.T) {
		db, mock := newFlattenMock(t)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1")).
			WillReturnRows(sqlmock.NewRows([]string{"commit_hash"}).AddRow(flattenInitialHash))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		if err := Flatten(context.Background(), db); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("statements: %v", err)
		}
	})
}
