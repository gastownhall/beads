package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestConcludeMergeRefusesIncompleteMergeCommit (GH#7757) pins the
// completeness contract of a merge conclusion: when the open merge's source
// cannot be read, a merge-touched table cannot be staged, or a merge-touched
// table is still unstaged after staging, concludeMerge reports an error and
// never runs the concluding DOLT_COMMIT. Committing then would record a merge
// commit that reverts the peer's rows on that table (the dropped comment of
// GH#7757) while the working set keeps reading them. The SettleMerge leg
// proves the caller then aborts the merge instead of reporting success.
func TestConcludeMergeRefusesIncompleteMergeCommit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expect  func(mock sqlmock.Sqlmock)
		wantErr string
	}{
		{
			name: "source query fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
					WillReturnError(errors.New("merge status unavailable"))
			},
			wantErr: "query merge source commit",
		},
		{
			name: "source missing",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
					WillReturnRows(sqlmock.NewRows([]string{"source_commit"}).AddRow(nil))
			},
			wantErr: "not a valid commit hash",
		},
		{
			name: "status query fails",
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeSourceCommit(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnError(errors.New("dolt_status unavailable"))
			},
			wantErr: "query merged-but-unstaged tables",
		},
		{
			name: "staging fails",
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeSourceCommit(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("comments"))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).WithArgs("comments").
					WillReturnError(errors.New("stage refused"))
			},
			wantErr: "stage merged comments",
		},
		{
			name: "table still unstaged after staging",
			expect: func(mock sqlmock.Sqlmock) {
				expectMergeSourceCommit(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("comments").AddRow("labels"))
				expectDrainedCall(mock, "CALL DOLT_ADD(?)", "comments")
				expectDrainedCall(mock, "CALL DOLT_ADD(?)", "labels")
				// The re-read after staging still names comments: a write
				// landed after the stage, or the stage did not take.
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("comments"))
			},
			wantErr: "comments",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, mock := newDrainMock(t)
			tc.expect(mock)

			err := concludeMerge(context.Background(), rec, "conclude", "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("concludeMerge() error = %v, want an error containing %q", err, tc.wantErr)
			}
			if rec.sentStatement("DOLT_COMMIT") {
				t.Error("concludeMerge ran DOLT_COMMIT over an incomplete merge result")
			}
			rec.assertDrained(t, mock)
		})
	}

	t.Run("settle merge aborts", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectConflicts(mock, "metadata")
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")
		expectDrainedCall(mock, "CALL DOLT_ADD('metadata')")
		expectNoViolations(mock)
		expectMergeSourceCommit(mock)
		mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
			WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("comments"))
		expectDrainedCall(mock, "CALL DOLT_ADD(?)", "comments")
		mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
			WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("comments"))
		expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")

		err := SettleMerge(context.Background(), rec, nil, false, "")
		if err == nil || !strings.Contains(err.Error(), "comments") {
			t.Fatalf("SettleMerge() error = %v, want the still-unstaged comments table named", err)
		}
		if rec.sentStatement("DOLT_COMMIT") {
			t.Error("SettleMerge committed an incomplete merge result")
		}
		rec.assertDrained(t, mock)
	})
}
