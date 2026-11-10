package dolt

import (
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestCLIPullSettlementSafety pins the declined-CLI-pull settlement's safety
// contract (GH#7839) with sqlmock, where the exact statement sequence — and
// the statements that must NOT happen — is observable:
//
//   - a merge this pull opened is aborted, with the conflicts captured
//     BEFORE the abort (the abort empties dolt_conflicts, bd-578h9.15) and
//     the abort CALL drained as a query;
//   - a merge that was already open before the transport ran is NEVER
//     aborted — DOLT_MERGE('--abort') would discard an operator's
//     in-progress resolution — and never reported as *MergeConflictsError,
//     whose contract promises a restoration that would not have happened;
//   - an abort that fails is reported, never papered over with the
//     preMergeClean-gated DOLT_RESET fallback (bd-578h9.2);
//   - a read-only store passes the transport error through untouched;
//   - a transport failure with no merge and nothing to resolve returns the
//     transport error unchanged, with no abort attempted.
//
// The conflict class is an off-allowlist table ("wisps"): the resolver
// declines it without further queries, so each sequence pins exactly the
// settlement's own statements.
func TestCLIPullSettlementSafety(t *testing.T) {
	newMockStore := func(t *testing.T, readOnly bool) (*DoltStore, sqlmock.Sqlmock) {
		t.Helper()
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return &DoltStore{db: db, readOnly: readOnly}, mock
	}
	expectResolverDecline := func(mock sqlmock.Sqlmock) {
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectBegin()
		// The resolver screens dolt_conflicts and declines the table.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("wisps", 1))
		// The decline capture: the same query again, before the rollback.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("wisps", 1))
		mock.ExpectRollback()
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMergeStatus := func(mock sqlmock.Sqlmock, merging bool) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT is_merging FROM dolt_merge_status")).
			WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(merging))
	}

	t.Run("abort a merge this pull opened, conflicts captured pre-abort", func(t *testing.T) {
		store, mock := newMockStore(t, false)
		pullErr := errors.New("dolt pull failed: Automatic merge failed")
		expectMergeStatus(mock, false) // pre-transport read: no merge open
		expectResolverDecline(mock)
		expectMergeStatus(mock, true) // the pull's merge is open now
		// The abort CALL returns a result set; it must be drained as a query.
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
			WillReturnRows(sqlmock.NewRows([]string{"status"}))

		err := store.runCLIPull(t.Context(), func() error { return pullErr })
		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("runCLIPull error = %v, want *MergeConflictsError", err)
		}
		if len(mce.Conflicts) != 1 || mce.Conflicts[0].Field != "wisps" {
			t.Errorf("MergeConflictsError.Conflicts = %v, want the captured wisps conflict", mce.Conflicts)
		}
		if !errors.Is(err, pullErr) {
			t.Error("the conflicts error does not wrap the pull's own error")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("pre-existing merge is never aborted", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			prior func(sqlmock.Sqlmock)
		}{
			{name: "merge open before the transport", prior: func(mock sqlmock.Sqlmock) {
				expectMergeStatus(mock, true)
			}},
			{name: "pre-read unreadable", prior: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT is_merging FROM dolt_merge_status")).
					WillReturnError(errors.New("merge status unreadable"))
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store, mock := newMockStore(t, false)
				pullErr := errors.New("dolt pull failed: You have an unresolved merge")
				tc.prior(mock)
				expectResolverDecline(mock)
				expectMergeStatus(mock, true) // still the operator's merge

				err := store.runCLIPull(t.Context(), func() error { return pullErr })
				var mce *versioncontrolops.MergeConflictsError
				if errors.As(err, &mce) {
					t.Fatalf("runCLIPull error = %v, want a diagnostic, not *MergeConflictsError: its contract promises a restoration that never happened", err)
				}
				if !errors.Is(err, pullErr) {
					t.Errorf("error does not wrap the pull's own error: %v", err)
				}
				if msg := err.Error(); !regexp.MustCompile(`already open`).MatchString(msg) ||
					!regexp.MustCompile(`DOLT_MERGE\('--abort'\)`).MatchString(msg) {
					t.Errorf("error %q does not name the pre-existing merge and the manual abort remedy", msg)
				}
				// The expectations above include no DOLT_MERGE('--abort'):
				// a call to it would fail ExpectationsWereMet as unexpected.
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet sqlmock expectations: %v", err)
				}
			})
		}
	})

	t.Run("abort failure is reported, never reset", func(t *testing.T) {
		store, mock := newMockStore(t, false)
		pullErr := errors.New("dolt pull failed: Automatic merge failed")
		expectMergeStatus(mock, false)
		expectResolverDecline(mock)
		expectMergeStatus(mock, true)
		abortErr := errors.New("merge abort refused")
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
			WillReturnError(abortErr)

		err := store.runCLIPull(t.Context(), func() error { return pullErr })
		var mce *versioncontrolops.MergeConflictsError
		if errors.As(err, &mce) {
			t.Fatalf("runCLIPull error = %v, want the abort failure reported, not a conflicts error", err)
		}
		if !errors.Is(err, pullErr) || !errors.Is(err, abortErr) {
			t.Errorf("error = %v, want the pull's and the abort's own errors joined", err)
		}
		if msg := err.Error(); !regexp.MustCompile(`still open`).MatchString(msg) {
			t.Errorf("error %q does not say the merge is still open", msg)
		}
		// No DOLT_RESET('--hard') expectation: the settlement cannot know
		// whether the working set was clean before the merge, and a reset
		// would destroy uncommitted data (bd-578h9.2).
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("read-only store passes the transport error through", func(t *testing.T) {
		store, mock := newMockStore(t, true)
		pullErr := errors.New("dolt pull failed: Automatic merge failed")

		err := store.runCLIPull(t.Context(), func() error { return pullErr })
		if !errors.Is(err, pullErr) || err.Error() != pullErr.Error() {
			t.Fatalf("runCLIPull error = %v, want the transport error untouched", err)
		}
		// No expectations at all: a read-only store touches nothing.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("transport failure with no merge returns the transport error", func(t *testing.T) {
		store, mock := newMockStore(t, false)
		pullErr := errors.New("dolt pull failed: connection reset by peer")
		expectMergeStatus(mock, false) // pre-transport read: no merge open
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectBegin()
		// No conflicts at all.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
		// The decline gate's capture query, also empty.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
		// The FK repair screen, also empty.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
			WillReturnRows(sqlmock.NewRows([]string{"table"}))
		mock.ExpectRollback()
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		err := store.runCLIPull(t.Context(), func() error { return pullErr })
		if !errors.Is(err, pullErr) || err.Error() != pullErr.Error() {
			t.Fatalf("runCLIPull error = %v, want the transport error unchanged", err)
		}
		var mce *versioncontrolops.MergeConflictsError
		if errors.As(err, &mce) {
			t.Fatal("a pull with no conflicts must not report *MergeConflictsError")
		}
		// No abort expectation: with no merge open, none is attempted.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})
}
