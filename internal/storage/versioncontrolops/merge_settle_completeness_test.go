package versioncontrolops

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// The GH#7757 conclusion-completeness contract (design AC-5): the merge
// conclusion must not publish a known incomplete result. stageMergeResult
// re-reads dolt_status after staging, and a merge-touched table still
// unstaged — a staging that silently missed, an ignored-table edge, a Dolt
// rename quirk — fails the conclusion naming the table instead of letting
// DOLT_COMMIT record HEAD's copy of the survivor (the exact revert GH#7811's
// staging exists to prevent). Query, staging, and source failures fail the
// same way: error out, commit nothing; the caller then aborts the merge
// (embedded) or rolls back its transaction (server mode).

// expectUnstagedTables answers one unstagedMergeTables read with tables.
func expectUnstagedTables(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table_name"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).WillReturnRows(rows)
}

// expectIncompleteMergeConclusion answers stageMergeResult's reads as far as
// its staging pass: the open merge's source commit, then the merged-but-
// unstaged tables, each staged with a drained DOLT_ADD.
func expectIncompleteMergeConclusion(mock sqlmock.Sqlmock, tables ...string) {
	expectMergeSourceCommit(mock)
	expectUnstagedTables(mock, tables...)
	for _, table := range tables {
		expectDrainedCall(mock, "CALL DOLT_ADD(?)", table)
	}
}

func TestConcludeMergeRefusesIncompleteMergeResult(t *testing.T) {
	rec, mock := newDrainMock(t)
	expectIncompleteMergeConclusion(mock, "labels")
	// The completeness re-read still names labels: staging did not clear it,
	// so the conclusion must fail instead of committing a revert of it.
	expectUnstagedTables(mock, "labels")

	err := concludeMerge(t.Context(), rec, "conclude", "")
	if err == nil {
		t.Fatal("concludeMerge() = nil, want the unstaged merge-touched table to fail it")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Errorf("concludeMerge() error = %v, want it to name the unstaged table", err)
	}
	// No DOLT_COMMIT was expected: sqlmock's ordered mode fails the test if
	// one ran, and ExpectationsWereMet proves the reads ran as described.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
	if rec.sentStatement("DOLT_COMMIT") {
		t.Error("concludeMerge committed despite a merge-touched table remaining unstaged")
	}
}

// TestConcludeMergeErrorsBeforeCommitting pins the pre-commit failure paths
// of the staging pass (AC-5): the merge source read, the merged-but-unstaged
// tables read, and an invalid (uninlinable) source commit each fail the
// conclusion without any commit, so no incomplete merge commit exists for a
// later push to publish.
func TestConcludeMergeErrorsBeforeCommitting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(mock sqlmock.Sqlmock)
	}{
		{
			name: "merge source query failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
					WillReturnError(errors.New("injected"))
			},
		},
		{
			name: "merged tables query failure",
			setup: func(mock sqlmock.Sqlmock) {
				expectMergeSourceCommit(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnError(errors.New("injected"))
			},
		},
		{
			name: "invalid merge source commit",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
					WillReturnRows(sqlmock.NewRows([]string{"source_commit"}).AddRow("not-a-hash"))
			},
		},
		{
			name: "completeness re-read query failure",
			setup: func(mock sqlmock.Sqlmock) {
				expectIncompleteMergeConclusion(mock, "labels")
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnError(errors.New("injected"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, mock := newDrainMock(t)
			tc.setup(mock)

			err := concludeMerge(t.Context(), rec, "conclude", "")
			if err == nil {
				t.Fatal("concludeMerge() = nil, want the injected query failure to fail it")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet sqlmock expectations: %v", err)
			}
			if rec.sentStatement("DOLT_COMMIT") {
				t.Error("concludeMerge committed despite the staging-pass failure")
			}
		})
	}
}

// TestMergeAndSettleAbortsWhenConclusionIsIncomplete pins the embedded-mode
// failure contract (AC-5): a conclusion that refuses an incomplete merge
// result must leave the caller's abort in charge — MergeAndSettle returns the
// error and the merge is aborted (the autocommit-mode stand-in for server
// mode's rollback), not left open with a half-staged working set.
func TestMergeAndSettleAbortsWhenConclusionIsIncomplete(t *testing.T) {
	rec, mock := newDrainMock(t)
	expectWorkingSetClean(mock)
	expectConflictFlags(mock)
	expectDrainedCall(mock, "CALL DOLT_MERGE(?)", "origin/main")
	expectConflicts(mock, "metadata")
	expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")
	expectDrainedCall(mock, "CALL DOLT_ADD('metadata')")
	expectNoViolations(mock)
	expectIncompleteMergeConclusion(mock, "labels")
	expectUnstagedTables(mock, "labels")
	expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")

	err := MergeAndSettleWithStrategy(t.Context(), rec, "origin/main", "")
	if err == nil {
		t.Fatal("MergeAndSettleWithStrategy() = nil, want the incomplete conclusion to fail it")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Errorf("MergeAndSettleWithStrategy() error = %v, want it to name the unstaged table", err)
	}
	if !rec.sentStatement("DOLT_MERGE('--abort')") {
		t.Error("MergeAndSettleWithStrategy() did not abort the refused merge")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
	if rec.sentStatement("DOLT_COMMIT") {
		t.Error("MergeAndSettleWithStrategy() committed despite the incomplete merge result")
	}
}
