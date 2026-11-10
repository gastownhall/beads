package dolt

import (
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// expectCLIRepairUpToCommit is the CLI settle pass auto-resolving a metadata
// conflict up to (not including) its DOLT_COMMIT, as in
// TestServerSettlementSQLCommitResponseLossIsIndeterminate's CLI subtest.
func expectCLIRepairUpToCommit(mock sqlmock.Sqlmock) {
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
		WillReturnRows(sqlmock.NewRows([]string{"source_commit"}).AddRow("0123456789abcdefghijklmnopqrstuv"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT CASE WHEN s.table_name = CONCAT(d.from_table_name, ' -> ', d.to_table_name)")).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}))
}

// TestFinishCLIPullCommitFailurePreservesOutcome pins AC-6 of GH#7839: when
// the CLI settle pass's repair fails at either commit boundary, finishCLIPull
// runs no further inspection and no abort (the commit may have landed), and a
// commit whose outcome is indeterminate keeps ErrCommitIndeterminate in the
// returned chain even when the pull itself also reported an error.
func TestFinishCLIPullCommitFailurePreservesOutcome(t *testing.T) {
	pullErr := errors.New("dolt pull: merge conflicts")
	for _, boundary := range []struct {
		name          string
		cause         error
		indeterminate bool
		expect        func(sqlmock.Sqlmock, error)
	}{
		{
			name:  "DOLT_COMMIT fails",
			cause: errors.New("DOLT_COMMIT refused"),
			expect: func(mock sqlmock.Sqlmock, cause error) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).WillReturnError(cause)
				mock.ExpectRollback()
			},
		},
		{
			name:          "SQL commit response lost",
			cause:         errors.New("CLI repair SQL commit response lost"),
			indeterminate: true,
			expect: func(mock sqlmock.Sqlmock, cause error) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectCommit().WillReturnError(cause)
			},
		},
	} {
		for _, pe := range []error{nil, pullErr} {
			name := boundary.name + "/pull succeeded"
			if pe != nil {
				name = boundary.name + "/pull failed"
			}
			t.Run(name, func(t *testing.T) {
				db, mock := newSettlementMock(t)
				expectCLIRepairUpToCommit(mock)
				boundary.expect(mock, boundary.cause)
				mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 0")).
					WillReturnResult(sqlmock.NewResult(0, 0))
				// Anything after this — a merge-state read, an abort — is
				// unexpected: sqlmock fails it, and the error surfaces below.

				store := &DoltStore{db: db}
				err := store.finishCLIPull(t.Context(), pe)
				if err == nil {
					t.Fatal("finishCLIPull = nil after a failed repair commit")
				}
				var mce *versioncontrolops.MergeConflictsError
				if errors.As(err, &mce) {
					t.Fatalf("finishCLIPull = %v; a failed repair commit is not a declined merge", err)
				}
				if boundary.indeterminate {
					assertPublicCommitIndeterminate(t, err, boundary.cause)
				} else if errors.Is(err, storage.ErrCommitIndeterminate) {
					t.Fatalf("finishCLIPull = %v; a refused DOLT_COMMIT is not indeterminate", err)
				}
				switch {
				case pe != nil && !errors.Is(err, pe):
					t.Fatalf("finishCLIPull = %v, want the pull's error %v in the chain", err, pe)
				case pe == nil && !errors.Is(err, boundary.cause):
					t.Fatalf("finishCLIPull = %v, want the commit failure %v", err, boundary.cause)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet sqlmock expectations: %v", err)
				}
			})
		}
	}
}
