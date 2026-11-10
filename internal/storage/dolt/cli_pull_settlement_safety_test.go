package dolt

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

const (
	mergeStatusQuery = "SELECT is_merging FROM dolt_merge_status"
	mergeAbortCall   = "CALL DOLT_MERGE('--abort')"
)

func newSettlementMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// expectDeclinedInspection is the CLI settle pass's inspection of a working
// set holding a conflict on a table the resolver never auto-resolves: the
// resolver declines, the conflicts are captured, and only then does the
// inspection transaction roll back.
func expectDeclinedInspection(mock sqlmock.Sqlmock) {
	conflicts := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("widgets", 2)
	}
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(conflicts())
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(conflicts())
	mock.ExpectRollback()
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectMergeState(mock sqlmock.Sqlmock, merging bool) {
	mock.ExpectQuery(regexp.QuoteMeta(mergeStatusQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(merging))
}

func expectAbort(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(mergeAbortCall)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
}

// assertNotRestored fails unless err reports a declined merge bd left alone:
// no MergeConflictsError, no claim that the working set was restored, the
// live remedies named, and the pull's own error still in the chain.
func assertNotRestored(t *testing.T, err, pullErr error) {
	t.Helper()
	if err == nil {
		t.Fatal("settlement returned nil for a declined merge it did not abort")
	}
	var mce *versioncontrolops.MergeConflictsError
	if errors.As(err, &mce) {
		t.Fatalf("settlement = %v; a merge bd did not abort must not be reported as a MergeConflictsError", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "restored") {
		t.Fatalf("settlement error %q claims restoration", msg)
	}
	for _, want := range []string{"did not abort", "bd conflicts list"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("settlement error %q does not mention %q", msg, want)
		}
	}
	if pullErr != nil && !errors.Is(err, pullErr) {
		t.Fatalf("settlement error %v does not wrap the pull's error %v", err, pullErr)
	}
}

// TestCLIPullSettlementSafety pins the guard rails on the GH#7839 abort (AC-5):
// a declined CLI-pull merge is aborted only when this pull is known to have
// opened it and the abort is confirmed; every other outcome leaves the working
// set alone — no abort of a pre-existing merge, no DOLT_RESET — and never
// reports the merge as restored.
func TestCLIPullSettlementSafety(t *testing.T) {
	pullErr := errors.New("dolt pull: merge conflicts")
	readErr := errors.New("connection reset")
	abortErr := errors.New("abort refused")
	noMergeStatusRow := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"is_merging"}) }

	for _, tc := range []struct {
		name   string
		prior  cliPullMergeState
		expect func(sqlmock.Sqlmock)
		// cause is the cleanup failure the diagnostic must keep in its
		// chain beside the pull's error.
		cause error
	}{
		{name: "merge already open before the pull", prior: mergeStateOpen},
		{name: "merge state before the pull unknown", prior: mergeStateUnknown},
		{
			name:  "merge closed again before the abort",
			prior: mergeStateClosed,
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeState(mock, false)
			},
		},
		{
			name:  "merge state after the pull unknown",
			prior: mergeStateClosed,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(mergeStatusQuery)).WillReturnError(readErr)
			},
			cause: readErr,
		},
		{
			name:  "merge state after the pull has no row",
			prior: mergeStateClosed,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(mergeStatusQuery)).WillReturnRows(noMergeStatusRow())
			},
			cause: sql.ErrNoRows,
		},
		{
			name:  "abort fails",
			prior: mergeStateClosed,
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeState(mock, true)
				mock.ExpectQuery(regexp.QuoteMeta(mergeAbortCall)).WillReturnError(abortErr)
			},
			cause: abortErr,
		},
		{
			name:  "merge state after the abort unreadable",
			prior: mergeStateClosed,
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeState(mock, true)
				expectAbort(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergeStatusQuery)).WillReturnError(readErr)
			},
			cause: readErr,
		},
		{
			name:  "merge state after the abort has no row",
			prior: mergeStateClosed,
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeState(mock, true)
				expectAbort(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergeStatusQuery)).WillReturnRows(noMergeStatusRow())
			},
			cause: sql.ErrNoRows,
		},
		{
			name:  "abort not confirmed",
			prior: mergeStateClosed,
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeState(mock, true)
				expectAbort(mock)
				expectMergeState(mock, true)
			},
		},
	} {
		for _, pe := range []error{pullErr, nil} {
			name := tc.name + "/pull failed"
			if pe == nil {
				name = tc.name + "/pull succeeded"
			}
			t.Run(name, func(t *testing.T) {
				db, mock := newSettlementMock(t)
				expectDeclinedInspection(mock)
				if tc.expect != nil {
					tc.expect(mock)
				}
				store := &DoltStore{db: db}
				err := store.settleCLIPull(t.Context(), tc.prior, pe)
				assertNotRestored(t, err, pe)
				if tc.cause != nil && !errors.Is(err, tc.cause) {
					t.Fatalf("settlement error %v does not keep the cleanup failure %v in its chain", err, tc.cause)
				}
				// Any further statement — an abort of a merge bd does not own,
				// or a DOLT_RESET fallback — would be unexpected and fail here
				// or surface in err above.
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet sqlmock expectations: %v", err)
				}
			})
		}
	}

	t.Run("confirmed abort reports the captured conflicts", func(t *testing.T) {
		db, mock := newSettlementMock(t)
		expectDeclinedInspection(mock)
		expectMergeState(mock, true)
		expectAbort(mock)
		expectMergeState(mock, false)
		store := &DoltStore{db: db}
		err := store.settleCLIPull(t.Context(), mergeStateClosed, pullErr)
		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("settleCLIPull = %v, want *MergeConflictsError", err)
		}
		if len(mce.Conflicts) != 1 || mce.Conflicts[0].Field != "widgets" || mce.Conflicts[0].Count != 2 {
			t.Fatalf("MergeConflictsError.Conflicts = %+v, want the captured widgets conflict", mce.Conflicts)
		}
		if !errors.Is(err, pullErr) {
			t.Fatalf("MergeConflictsError %v does not wrap the pull's error", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("conflict capture fails", func(t *testing.T) {
		for _, pe := range []error{pullErr, nil} {
			db, mock := newSettlementMock(t)
			captureErr := errors.New("dolt_conflicts read failed")
			mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
				WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("widgets", 2))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
				WillReturnError(captureErr)
			mock.ExpectRollback()
			mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			store := &DoltStore{db: db}
			err := store.settleCLIPull(t.Context(), mergeStateClosed, pe)
			want := pe
			if want == nil {
				want = captureErr
			}
			if !errors.Is(err, want) {
				t.Fatalf("settleCLIPull = %v, want %v", err, want)
			}
			var mce *versioncontrolops.MergeConflictsError
			if errors.As(err, &mce) {
				t.Fatalf("settleCLIPull = %v; nothing was captured, so nothing may be reported as aborted", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		}
	})

	t.Run("inspection rollback fails after capturing conflicts", func(t *testing.T) {
		for _, pe := range []error{pullErr, nil} {
			db, mock := newSettlementMock(t)
			rollbackErr := errors.New("rollback lost the connection")
			mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectBegin()
			for range 2 {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
					WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("widgets", 2))
			}
			mock.ExpectRollback().WillReturnError(rollbackErr)
			mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			store := &DoltStore{db: db}
			err := store.settleCLIPull(t.Context(), mergeStateClosed, pe)
			// An ordinary error: the pull's own error keeps precedence, and
			// no merge-state read or abort follows (sqlmock would refuse it).
			want := pe
			if want == nil {
				want = rollbackErr
			}
			if !errors.Is(err, want) {
				t.Fatalf("settleCLIPull = %v, want %v", err, want)
			}
			var mce *versioncontrolops.MergeConflictsError
			if errors.As(err, &mce) {
				t.Fatalf("settleCLIPull = %v; a failed inspection rollback must not be reported as an abort", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		}
	})

	t.Run("inspection rollback fails after naming violations", func(t *testing.T) {
		for _, pe := range []error{pullErr, nil} {
			db, mock := newSettlementMock(t)
			rollbackErr := errors.New("rollback lost the connection")
			violated := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"table"}).AddRow("widgets") }
			mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectBegin()
			for range 2 {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
					WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
			}
			// The repair declines a table outside its allowlist; the decline
			// then names the violated tables.
			for range 2 {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
					WillReturnRows(violated())
			}
			mock.ExpectRollback().WillReturnError(rollbackErr)
			mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			store := &DoltStore{db: db}
			err := store.settleCLIPull(t.Context(), mergeStateClosed, pe)
			want := pe
			if want == nil {
				want = rollbackErr
			}
			if !errors.Is(err, want) {
				t.Fatalf("settleCLIPull = %v, want %v", err, want)
			}
			if strings.Contains(err.Error(), "merge aborted") {
				t.Fatalf("settleCLIPull = %v; a failed inspection rollback must not be reported as an abort", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		}
	})

	t.Run("transport failure with nothing merged", func(t *testing.T) {
		db, mock := newSettlementMock(t)
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectBegin()
		for range 2 {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
				WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
			WillReturnRows(sqlmock.NewRows([]string{"table"}))
		mock.ExpectRollback()
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		transport := errors.New("dolt pull: remote unreachable")
		store := &DoltStore{db: db}
		if err := store.settleCLIPull(t.Context(), mergeStateClosed, transport); err != transport {
			t.Fatalf("settleCLIPull = %v, want the transport error unchanged", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("read-only store runs no SQL", func(t *testing.T) {
		db, mock := newSettlementMock(t)
		store := &DoltStore{db: db, readOnly: true}
		ran := false
		err := store.runCLIPull(t.Context(), func() error { ran = true; return pullErr })
		if !ran || err != pullErr {
			t.Fatalf("runCLIPull on a read-only store = %v (pull ran: %v), want the pull's error as-is", err, ran)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("unreadable prior state is never aborted", func(t *testing.T) {
		for _, prior := range []struct {
			name   string
			expect func(sqlmock.Sqlmock)
		}{
			{"query error", func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(mergeStatusQuery)).WillReturnError(readErr)
			}},
			{"no row", func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(mergeStatusQuery)).WillReturnRows(noMergeStatusRow())
			}},
		} {
			t.Run(prior.name, func(t *testing.T) {
				db, mock := newSettlementMock(t)
				prior.expect(mock)
				store := &DoltStore{db: db}
				err := store.runCLIPull(t.Context(), func() error {
					expectDeclinedInspection(mock)
					return pullErr
				})
				assertNotRestored(t, err, pullErr)
				if !strings.Contains(err.Error(), "before this pull could not be read") {
					t.Fatalf("runCLIPull = %v, want the unreadable prior state named", err)
				}
				// No merge-state re-read and no abort follow (sqlmock would
				// refuse them).
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet sqlmock expectations: %v", err)
				}
			})
		}
	})

	t.Run("prior state is read before the pull on a one-connection pool", func(t *testing.T) {
		db, mock := newSettlementMock(t)
		// A settle pass that held one pooled connection while asking for
		// another would block here until the deadline.
		db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		expectMergeState(mock, false)
		store := &DoltStore{db: db}
		err := store.runCLIPull(ctx, func() error {
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("merge state was not read before the pull ran: %v", err)
			}
			expectDeclinedInspection(mock)
			expectMergeState(mock, true)
			expectAbort(mock)
			expectMergeState(mock, false)
			return pullErr
		})
		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("runCLIPull = %v, want *MergeConflictsError", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})
}
