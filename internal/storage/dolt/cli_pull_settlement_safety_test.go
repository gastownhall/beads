package dolt

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

const openMergeQuery = "SELECT is_merging, source, target FROM dolt_merge_status"

func newSettlementMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// expectNoOpenMerge is the merge transaction's first look at the working set
// finding no merge: source and target are NULL.
func expectNoOpenMerge(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(openMergeQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"is_merging", "source", "target"}).AddRow(false, nil, nil))
}

// expectFetchedMerge is the merge of origin's main, which the CLI fetch just
// refreshed, returning DOLT_MERGE's row.
func expectFetchedMerge(mock sqlmock.Sqlmock, conflicts int, message string) {
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE(?)")).WithArgs("origin/main").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "fast_forward", "conflicts", "message"}).
			AddRow("", 0, conflicts, message))
}

// expectMetadataRepairUpToCommit is the settle pass auto-resolving a metadata
// conflict up to (not including) its DOLT_COMMIT, as in
// TestServerSettlementSQLCommitResponseLossIsIndeterminate.
func expectMetadataRepairUpToCommit(mock sqlmock.Sqlmock) {
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

// rejectUnexpected fails when err wraps sqlmock's refusal of a statement it
// was not told to expect (a merge over another client's merge, an abort, a
// DOLT_RESET): the code under test would otherwise carry it as an ordinary
// error.
func rejectUnexpected(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "was not expected") {
		t.Fatalf("error %q wraps a statement sqlmock was not told to expect", err)
	}
}

// TestCLIPullSettlementSafety pins how the CLI routes merge what their `dolt
// fetch` brought in (GH#7839): inside bd's own transaction (mergeFetchedInTx),
// so a merge the resolver declines rolls back with it, its conflicts captured
// first, and nothing is aborted afterwards; a merge another client already
// has open in the working set is neither merged over, settled nor aborted;
// and a settle commit DOLT_COMMIT refuses is reported as such, not as
// indeterminate (AC-6; a lost commit response is, as
// TestServerSettlementSQLCommitResponseLossIsIndeterminate pins). Every
// statement is expected in order, so an abort, a DOLT_RESET, or a merge over
// the open merge fails the test.
func TestCLIPullSettlementSafety(t *testing.T) {
	readErr := errors.New("connection reset")
	commitRefused := errors.New("DOLT_COMMIT refused")
	widgets := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("widgets", 2)
	}
	empty := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"table", "num_conflicts"}) }

	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		check  func(*testing.T, pullReport, error)
	}{
		{
			name: "another client's open merge is left alone",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(openMergeQuery)).
					WillReturnRows(sqlmock.NewRows([]string{"is_merging", "source", "target"}).
						AddRow(true, "refs/remotes/origin/main", "refs/heads/main"))
				mock.ExpectRollback()
			},
			check: func(t *testing.T, _ pullReport, err error) {
				var mce *versioncontrolops.MergeConflictsError
				if err == nil || errors.As(err, &mce) {
					t.Fatalf("merge = %v; another client's open merge must be refused, not settled", err)
				}
				want := "a merge of refs/remotes/origin/main into refs/heads/main is already open in the working set, so bd did not merge origin/main"
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("merge error %q does not mention %q", err, want)
				}
				if strings.Contains(err.Error(), "restored") {
					t.Fatalf("merge error %q claims a restoration it did not make", err)
				}
			},
		},
		{
			name: "an unreadable merge state merges nothing",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(openMergeQuery)).WillReturnError(readErr)
				mock.ExpectRollback()
			},
			check: func(t *testing.T, _ pullReport, err error) {
				if !errors.Is(err, readErr) {
					t.Fatalf("merge = %v, want the merge-state read failure %v", err, readErr)
				}
			},
		},
		{
			name: "a declined conflict rolls back with no abort",
			expect: func(mock sqlmock.Sqlmock) {
				expectNoOpenMerge(mock)
				expectFetchedMerge(mock, 1, "conflicts found")
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(widgets())
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(widgets())
				mock.ExpectRollback()
			},
			check: func(t *testing.T, report pullReport, err error) {
				var mce *versioncontrolops.MergeConflictsError
				if !errors.As(err, &mce) {
					t.Fatalf("merge = %v, want *MergeConflictsError", err)
				}
				if tables := declinedTables(mce.Conflicts); !slices.Equal(tables, []string{"widgets"}) {
					t.Fatalf("MergeConflictsError names %v, want [widgets]", tables)
				}
				if !report.Conflicted {
					t.Fatalf("report = %+v, want DOLT_MERGE's conflicts flag", report)
				}
			},
		},
		{
			name: "a clean merge commits",
			expect: func(mock sqlmock.Sqlmock) {
				expectNoOpenMerge(mock)
				expectFetchedMerge(mock, 0, doltMergeSucceededMessage)
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(empty())
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(empty())
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
					WillReturnRows(sqlmock.NewRows([]string{"table"}))
				mock.ExpectCommit()
			},
			check: func(t *testing.T, report pullReport, err error) {
				if err != nil {
					t.Fatalf("merge = %v, want nil", err)
				}
				if !report.Reported || !report.Merged {
					t.Fatalf("report = %+v, want DOLT_MERGE's merged report", report)
				}
			},
		},
		{
			name: "a refused settle commit is reported, not indeterminate",
			expect: func(mock sqlmock.Sqlmock) {
				expectNoOpenMerge(mock)
				expectFetchedMerge(mock, 1, "conflicts found")
				expectMetadataRepairUpToCommit(mock)
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).WillReturnError(commitRefused)
				mock.ExpectRollback()
			},
			check: func(t *testing.T, _ pullReport, err error) {
				if !errors.Is(err, commitRefused) {
					t.Fatalf("merge = %v, want the commit failure %v", err, commitRefused)
				}
				if errors.Is(err, storage.ErrCommitIndeterminate) {
					t.Fatalf("merge = %v; a refused DOLT_COMMIT is not indeterminate", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSettlementMock(t)
			mock.ExpectBegin()
			tc.expect(mock)
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}

			store := &DoltStore{db: db, branch: "main"}
			report, err := store.mergeFetchedInTx(t.Context(), tx, "origin/main")
			rejectUnexpected(t, err)
			tc.check(t, report, err)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}

// TestCLIPullFailedFetchMergesNothing pins runCLIPull's order: a fetch that
// fails is the pull's error, and no merge runs over whatever the
// remote-tracking ref held before. This store has no server: a merge attempt
// would fail on its connection instead of returning the fetch's error.
func TestCLIPullFailedFetchMergesNothing(t *testing.T) {
	fetchErr := errors.New("dolt fetch failed: remote unreachable")
	store := &DoltStore{branch: "main"}
	report, err := store.runCLIPull(context.Background(), "origin", func() error { return fetchErr })
	if !errors.Is(err, fetchErr) {
		t.Fatalf("runCLIPull = %v, want the fetch's error %v", err, fetchErr)
	}
	if report.Reported {
		t.Fatalf("report = %+v after a failed fetch, want none", report)
	}
}
