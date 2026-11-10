package dolt

import (
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
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

// expectResolvableCLIMerge mocks the resolver pass over ONE auto-resolvable
// metadata conflict (GH#2466) with no FK violations, up to the settlement's
// two commit boundaries: CommitSettledMerge's CALL DOLT_COMMIT and the
// transaction's final Commit. failCommit selects which boundary loses its
// response ("settled" — the CALL DOLT_COMMIT — or "tx" — the final Commit).
func expectResolvableCLIMerge(mock sqlmock.Sqlmock, failCommit string, responseLoss error) {
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
		WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("metadata", 1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('metadata')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
		WillReturnRows(sqlmock.NewRows([]string{"table"}))
	// GH#7811: CommitSettledMerge concludes with the FULL merge result — it
	// reads the open merge's source commit, then the unstaged tables that
	// differ between HEAD and it (none here) — before its DOLT_COMMIT.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
		WillReturnRows(sqlmock.NewRows([]string{"source_commit"}).AddRow("0123456789abcdefghijklmnopqrstuv"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT CASE WHEN s.table_name = CONCAT(d.from_table_name, ' -> ', d.to_table_name) THEN d.to_table_name ELSE s.table_name END FROM dolt_status s JOIN (SELECT from_table_name, to_table_name FROM dolt_diff_summary('HEAD', '0123456789abcdefghijklmnopqrstuv') UNION SELECT from_table_name, to_table_name FROM dolt_schema_diff('HEAD', '0123456789abcdefghijklmnopqrstuv')) d ON s.table_name IN (d.from_table_name, d.to_table_name, CONCAT(d.from_table_name, ' -> ', d.to_table_name)) WHERE s.staged = 0")).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}))
	// concludeMerge binds the commit message and drains the CALL.
	if failCommit == "settled" {
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
			WithArgs("auto-resolve merge conflicts: metadata, dependencies, schema_migrations, config, issues (field-level three-way merge), labels/comments/events (union)").
			WillReturnError(responseLoss)
		mock.ExpectRollback()
	} else {
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
			WithArgs("auto-resolve merge conflicts: metadata, dependencies, schema_migrations, config, issues (field-level three-way merge), labels/comments/events (union)").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectCommit().WillReturnError(responseLoss)
	}
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// TestFinishCLIPullCommitFailurePreservesOutcome pins the deliberate
// exception in the declined-CLI-pull settlement: an INDETERMINATE commit
// during the resolve (a commit whose server response was lost, so the
// repair may have landed) must not be masked by the transport error. The
// no-replay sentinel (storage.ErrCommitIndeterminate) is what tells bd sync
// not to re-drive the pull, and both commit boundaries can lose it — the
// settlement commit's CALL DOLT_COMMIT and the resolve transaction's own
// final Commit. GH#7839's abort-and-report fix deliberately does NOT abort
// on these (an indeterminate commit may be the merge's conclusion), so all
// this path does is keep the sentinel in the returned error.
func TestFinishCLIPullCommitFailurePreservesOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failCommit string
		withPull   bool
	}{
		{name: "settled merge commit response lost, transport failed", failCommit: "settled", withPull: true},
		{name: "resolve tx commit response lost, transport failed", failCommit: "tx", withPull: true},
		{name: "settled merge commit response lost, transport succeeded", failCommit: "settled", withPull: false},
		{name: "resolve tx commit response lost, transport succeeded", failCommit: "tx", withPull: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			responseLoss := errors.New("SQL commit response lost")
			expectResolvableCLIMerge(mock, tc.failCommit, responseLoss)

			store := &DoltStore{db: db}
			var pullErr error
			if tc.withPull {
				pullErr = errors.New("dolt pull failed: Automatic merge failed")
			}
			err = store.finishCLIPull(t.Context(), pullErr)
			if !errors.Is(err, storage.ErrCommitIndeterminate) {
				t.Fatalf("finishCLIPull error = %v, want storage.ErrCommitIndeterminate preserved", err)
			}
			if !errors.Is(err, responseLoss) {
				t.Errorf("error = %v, want the lost-response cause preserved", err)
			}
			if pullErr != nil && !errors.Is(err, pullErr) {
				t.Errorf("error = %v, want the transport error preserved alongside the sentinel", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}
