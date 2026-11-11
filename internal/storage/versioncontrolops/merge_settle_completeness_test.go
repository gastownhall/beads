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
// unstaged — a staging that silently missed, a write that landed after the
// stage — fails the conclusion naming the table instead of letting
// DOLT_COMMIT record HEAD's copy of the survivor (the exact revert GH#7811's
// staging exists to prevent). A survivor an ignored = 1 dolt_ignore pattern
// matches is exempt: DOLT_ADD skips the tables Dolt ignores. Source, query
// and staging failures fail the same way: error out, commit nothing; the
// caller then aborts the merge (embedded) or rolls back its transaction
// (server mode).

// expectUnstagedTables answers one unstagedMergeTables read with tables.
func expectUnstagedTables(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table_name"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).WillReturnRows(rows)
}

// expectIgnorePatterns answers withoutIgnoreMatches' read of the ignored = 1
// dolt_ignore patterns.
func expectIgnorePatterns(mock sqlmock.Sqlmock, patterns ...string) {
	rows := sqlmock.NewRows([]string{"pattern"})
	for _, pattern := range patterns {
		rows.AddRow(pattern)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore WHERE ignored = 1")).WillReturnRows(rows)
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

// TestConcludeMergeRefusesIncompleteMergeResult pins the pre-commit failure
// paths of the staging pass (AC-5): the merge source read, a missing or
// uninlinable source commit, the merged-but-unstaged tables read, a staging
// call, the completeness re-read, its dolt_ignore read, and a merge-touched
// table the re-read still finds unstaged that no ignored = 1 pattern matches
// each fail the conclusion with a contextual error and without any commit,
// so no incomplete merge commit exists for a later push to publish.
func TestConcludeMergeRefusesIncompleteMergeResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(mock sqlmock.Sqlmock)
		wantErr string
	}{
		{
			name: "merge source query failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
					WillReturnError(errors.New("merge status unavailable"))
			},
			wantErr: "query merge source commit",
		},
		{
			name: "merge source missing",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
					WillReturnRows(sqlmock.NewRows([]string{"source_commit"}).AddRow(nil))
			},
			wantErr: "not a valid commit hash",
		},
		{
			name: "invalid merge source commit",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
					WillReturnRows(sqlmock.NewRows([]string{"source_commit"}).AddRow("not-a-hash"))
			},
			wantErr: "not a valid commit hash",
		},
		{
			name: "merged tables query failure",
			setup: func(mock sqlmock.Sqlmock) {
				expectMergeSourceCommit(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnError(errors.New("dolt_status unavailable"))
			},
			wantErr: "query merged-but-unstaged tables",
		},
		{
			name: "staging failure",
			setup: func(mock sqlmock.Sqlmock) {
				expectMergeSourceCommit(mock)
				expectUnstagedTables(mock, "comments")
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).WithArgs("comments").
					WillReturnError(errors.New("stage refused"))
			},
			wantErr: "stage merged comments",
		},
		{
			name: "completeness re-read query failure",
			setup: func(mock sqlmock.Sqlmock) {
				expectIncompleteMergeConclusion(mock, "labels")
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnError(errors.New("dolt_status unavailable"))
			},
			wantErr: "re-check merged-but-unstaged tables",
		},
		{
			name: "completeness re-read after an empty first read fails",
			setup: func(mock sqlmock.Sqlmock) {
				expectIncompleteMergeConclusion(mock)
				mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
					WillReturnError(errors.New("dolt_status unavailable"))
			},
			wantErr: "re-check merged-but-unstaged tables",
		},
		{
			name: "dolt_ignore read failure",
			setup: func(mock sqlmock.Sqlmock) {
				expectIncompleteMergeConclusion(mock, "comments")
				expectUnstagedTables(mock, "comments")
				mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore WHERE ignored = 1")).
					WillReturnError(errors.New("dolt_ignore unavailable"))
			},
			wantErr: "re-check merged-but-unstaged tables",
		},
		{
			name: "table still unstaged after staging",
			setup: func(mock sqlmock.Sqlmock) {
				expectIncompleteMergeConclusion(mock, "comments", "labels")
				// The re-read still names comments: a write landed after the
				// stage, or the stage did not take.
				expectUnstagedTables(mock, "comments")
				expectIgnorePatterns(mock, "__temp__%", "wisp_%")
			},
			wantErr: "comments",
		},
		{
			name: "only the survivors no ignored = 1 pattern matches refuse",
			setup: func(mock sqlmock.Sqlmock) {
				expectIncompleteMergeConclusion(mock, "__temp__probe", "labels")
				expectUnstagedTables(mock, "__temp__probe", "labels")
				expectIgnorePatterns(mock, "__temp__%")
			},
			wantErr: "merge-touched table(s) labels remain unstaged",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, mock := newDrainMock(t)
			tc.setup(mock)

			err := concludeMerge(t.Context(), rec, "conclude", "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("concludeMerge() error = %v, want an error containing %q", err, tc.wantErr)
			}
			// No DOLT_COMMIT was expected: sqlmock's ordered mode fails the
			// test if one ran, and assertDrained proves the reads ran as
			// described with every CALL drained.
			if rec.sentStatement("DOLT_COMMIT") {
				t.Error("concludeMerge ran DOLT_COMMIT over an incomplete merge result")
			}
			rec.assertDrained(t, mock)
		})
	}
}

// TestMergeAndSettleAbortsWhenConclusionIsIncomplete pins the embedded-mode
// failure contract (AC-5): a conclusion that refuses an incomplete merge
// result must leave the caller's abort in charge — MergeAndSettle and
// SettleMerge return the error and the merge is aborted (the autocommit-mode
// stand-in for server mode's rollback), not left open with a half-staged
// working set.
func TestMergeAndSettleAbortsWhenConclusionIsIncomplete(t *testing.T) {
	t.Run("MergeAndSettleWithStrategy", func(t *testing.T) {
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
		expectIgnorePatterns(mock)
		expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")

		err := MergeAndSettleWithStrategy(t.Context(), rec, "origin/main", "")
		if err == nil || !strings.Contains(err.Error(), "labels") {
			t.Fatalf("MergeAndSettleWithStrategy() error = %v, want the still-unstaged labels table named", err)
		}
		if !rec.sentStatement("DOLT_MERGE('--abort')") {
			t.Error("MergeAndSettleWithStrategy() did not abort the refused merge")
		}
		if rec.sentStatement("DOLT_COMMIT") {
			t.Error("MergeAndSettleWithStrategy() committed despite the incomplete merge result")
		}
		rec.assertDrained(t, mock)
	})

	t.Run("SettleMerge", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectConflicts(mock, "metadata")
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")
		expectDrainedCall(mock, "CALL DOLT_ADD('metadata')")
		expectNoViolations(mock)
		expectIncompleteMergeConclusion(mock, "comments")
		expectUnstagedTables(mock, "comments")
		expectIgnorePatterns(mock)
		expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")

		err := SettleMerge(t.Context(), rec, nil, false, "")
		if err == nil || !strings.Contains(err.Error(), "comments") {
			t.Fatalf("SettleMerge() error = %v, want the still-unstaged comments table named", err)
		}
		if rec.sentStatement("DOLT_COMMIT") {
			t.Error("SettleMerge committed an incomplete merge result")
		}
		rec.assertDrained(t, mock)
	})
}

// TestConcludeMergeExemptsIgnoredSurvivors pins the exemption: survivors that
// ignored = 1 patterns match, by Dolt's matching rules ('*' is a wildcard),
// are tables DOLT_ADD skips by design, so the conclusion commits rather than
// refusing a merge that every retry would refuse again.
func TestConcludeMergeExemptsIgnoredSurvivors(t *testing.T) {
	rec, mock := newDrainMock(t)
	expectIncompleteMergeConclusion(mock, "__temp__probe", "scratch_notes", "labels")
	expectUnstagedTables(mock, "__temp__probe", "scratch_notes")
	expectIgnorePatterns(mock, "__temp__%", "scratch*")
	expectDrainedCall(mock, "CALL DOLT_COMMIT('-m', ?)", "conclude")

	if err := concludeMerge(t.Context(), rec, "conclude", ""); err != nil {
		t.Fatalf("concludeMerge() error = %v, want the ignored survivors exempt", err)
	}
	rec.assertDrained(t, mock)
}

// TestDoltIgnorePatternREFollowsDoltMatching pins dolt_ignore pattern
// matching to Dolt's rules (doltdb's compilePattern), not LIKE's: '%' and '*'
// match any run of characters, '?' exactly one, and '_' and regexp
// metacharacters only themselves, case-sensitively.
func TestDoltIgnorePatternREFollowsDoltMatching(t *testing.T) {
	for _, tc := range []struct {
		pattern, table string
		want           bool
	}{
		{"__temp__%", "__temp__probe", true},
		{"__temp__%", "xxtempxxprobe", false}, // '_' is literal, not LIKE's wildcard
		{"comm%", "comments", true},
		{"scratch*", "scratch_notes", true}, // '*' is a wildcard, unlike in LIKE
		{"scratch*", "scratch", true},
		{"wisp?", "wisps", true},
		{"wisp?", "wisp", false},
		{"wisp?", "wisp_x", false},
		{"comments", "comments", true},
		{"comments", "Comments", false},
		{"a.c", "abc", false}, // regexp metacharacters are literal
		{"a+", "aa", false},
		{`a\?`, `a\x`, true}, // a backslash is literal and '?' still a wildcard
	} {
		re, err := doltIgnorePatternRE(tc.pattern)
		if err != nil {
			t.Fatalf("doltIgnorePatternRE(%q): %v", tc.pattern, err)
		}
		if got := re.MatchString(tc.table); got != tc.want {
			t.Errorf("pattern %q matches %q = %v, want %v", tc.pattern, tc.table, got, tc.want)
		}
	}
}
