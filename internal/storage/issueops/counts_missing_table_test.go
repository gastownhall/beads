package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// countsEntryPoint is one read path that answers a count (or a counted page)
// by merging the wisp plane into the durable one. Every one of them used to
// classify *any* table-not-exist error from its wisp query as "there are no
// wisps", which is only true for the tables a database may legitimately not
// have. The wisp queries reach past those: the counts mega-query's FROM
// carries sqlbuild.LeaseJoin, and hydration reads wisp_labels.
//
// prime installs the query expectations for one run, with wispErr returned by
// the wisp leg; run drives the entry point and renders the answer so a
// tolerating path reports what it silently produced rather than just "nil".
type countsEntryPoint struct {
	name string
	// missing lists the tables this entry point's wisp query can actually be
	// broken by, and the three lists below are deliberately not the same.
	// Every form reaches wisp_labels, through filter subqueries or the
	// by-label grouping. Beyond that the reach is the FROM clause's: the
	// plain COUNT(*) forms (countTableInTx, countByColumnInTx) build
	// `SELECT COUNT(*) FROM <wisps> [WHERE ...]` and nothing else, so they
	// stop there; searchTableInTxT adds sqlbuild.LeaseJoin, so leases joins
	// the list; and only the counts mega-query also LEFT JOINs the comment
	// table (sqlbuild/counts.go:198, the sole non-test use of
	// FilterTables.Comments), so wisp_comments is reachable there alone.
	missing       []string
	prime         func(mock sqlmock.Sqlmock, wispErr error)
	run           func(ctx context.Context, tx *sql.Tx) (string, error)
	wantTolerated string
}

func scalarCountRows(n int) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"count"}).AddRow(n)
}

func groupRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"k", "n"}).AddRow("open", 1)
}

// emptyCountsRows stands in for a counts mega-query that matched nothing.
// scanCountsRowsInTx never scans a column here, so the narrow column set is
// enough and the test does not have to restate the mega-query's projection.
func emptyCountsRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id"})
}

// wispPlaneAndLeases is the reach of a wide wisp projection: label subqueries
// plus the lease overlay searchTableInTxT adds (search.go:381).
var wispPlaneAndLeases = []string{"wisp_labels", "leases"}

// countsMegaQueryTables is the reach of sqlbuild.SearchCountsSQL, which is
// wispPlaneAndLeases plus the comment-count LEFT JOIN at
// sqlbuild/counts.go:198. Only the three entry points that render the counts
// mega-query can be broken by wisp_comments; the plain COUNT(*) forms never
// name it.
var countsMegaQueryTables = []string{"wisp_labels", "wisp_comments", "leases"}

var countsEntryPoints = []countsEntryPoint{
	{
		name:    "CountIssuesInTx/ephemeral",
		missing: []string{"wisp_labels"},
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM wisps`).WillReturnError(wispErr)
			// Only the tolerating path reaches the durable table. Priming it
			// makes the untightened behaviour a clean answer rather than an
			// unexpected-query error that would read like a pass.
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM issues`).WillReturnRows(scalarCountRows(1))
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			n, err := CountIssuesInTx(ctx, tx, "", types.IssueFilter{Ephemeral: boolPtr(true)})
			return fmt.Sprintf("count=%d", n), err
		},
		wantTolerated: "count=1",
	},
	{
		name:    "CountIssuesInTx/merge",
		missing: []string{"wisp_labels"},
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM issues`).WillReturnRows(scalarCountRows(1))
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM wisps`).WillReturnError(wispErr)
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			n, err := CountIssuesInTx(ctx, tx, "", types.IssueFilter{})
			return fmt.Sprintf("count=%d", n), err
		},
		wantTolerated: "count=1",
	},
	{
		name:    "CountIssuesByGroupInTx/ephemeral",
		missing: []string{"wisp_labels"},
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`(?s)FROM wisps.*GROUP BY status`).WillReturnError(wispErr)
			mock.ExpectQuery(`(?s)FROM issues.*GROUP BY status`).WillReturnRows(groupRows())
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			m, err := CountIssuesByGroupInTx(ctx, tx, types.IssueFilter{Ephemeral: boolPtr(true)}, "status")
			return fmt.Sprintf("groups=%v", m), err
		},
		wantTolerated: "groups=map[open:1]",
	},
	{
		name:    "CountIssuesByGroupInTx/merge",
		missing: []string{"wisp_labels"},
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`(?s)FROM issues.*GROUP BY status`).WillReturnRows(groupRows())
			mock.ExpectQuery(`(?s)FROM wisps.*GROUP BY status`).WillReturnError(wispErr)
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			m, err := CountIssuesByGroupInTx(ctx, tx, types.IssueFilter{}, "status")
			return fmt.Sprintf("groups=%v", m), err
		},
		wantTolerated: "groups=map[open:1]",
	},
	{
		name:    "SearchIssuesWithCountsInTx/ephemeral",
		missing: countsMegaQueryTables,
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`(?s)FROM wisps i`).WillReturnError(wispErr)
			mock.ExpectQuery(`(?s)FROM issues i`).WillReturnRows(emptyCountsRows())
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			out, err := SearchIssuesWithCountsInTx(ctx, tx, "", types.IssueFilter{Ephemeral: boolPtr(true)})
			return fmt.Sprintf("rows=%d", len(out)), err
		},
		wantTolerated: "rows=0",
	},
	{
		name:    "SearchIssuesWithCountsInTx/merge",
		missing: countsMegaQueryTables,
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`(?s)FROM issues i`).WillReturnRows(emptyCountsRows())
			mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`(?s)FROM wisps i`).WillReturnError(wispErr)
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			out, err := SearchIssuesWithCountsInTx(ctx, tx, "", types.IssueFilter{})
			return fmt.Sprintf("rows=%d", len(out)), err
		},
		wantTolerated: "rows=0",
	},
	{
		name:    "GetReadyWorkWithCountsInTx",
		missing: countsMegaQueryTables,
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`(?s)FROM issues i`).WillReturnRows(emptyCountsRows())
			mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`(?s)FROM wisps i`).WillReturnError(wispErr)
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			out, err := GetReadyWorkWithCountsInTx(ctx, tx, readyFilter())
			return fmt.Sprintf("rows=%d", len(out)), err
		},
		wantTolerated: "rows=0",
	},
	{
		name:    "CountReadyWorkInTx",
		missing: []string{"wisp_labels"},
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM issues`).WillReturnRows(scalarCountRows(1))
			mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM wisps`).WillReturnError(wispErr)
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			n, err := CountReadyWorkInTx(ctx, tx, readyFilter())
			return fmt.Sprintf("count=%d", n), err
		},
		wantTolerated: "count=1",
	},
	{
		name:    "GetReadyWorkInTx/unbounded",
		missing: wispPlaneAndLeases,
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT id FROM issues`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`(?s)FROM wisps`).WillReturnError(wispErr)
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			out, err := GetReadyWorkInTx(ctx, tx, readyFilter())
			return fmt.Sprintf("rows=%d", len(out)), err
		},
		wantTolerated: "rows=0",
	},
	{
		name:    "GetReadyWorkInTx/paged",
		missing: []string{"wisp_labels"},
		prime: func(mock sqlmock.Sqlmock, wispErr error) {
			mock.ExpectQuery(`SELECT id FROM issues`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`(?s)FROM wisps`).WillReturnError(wispErr)
		},
		run: func(ctx context.Context, tx *sql.Tx) (string, error) {
			filter := readyFilter()
			filter.Limit = 5
			out, err := GetReadyWorkInTx(ctx, tx, filter)
			return fmt.Sprintf("rows=%d", len(out)), err
		},
		wantTolerated: "rows=0",
	},
}

// readyFilter keeps the ready-work predicate builder query-free
// (IncludeDeferred skips the deferred-parent scan, and no ParentID means no
// descendant walk) so each expectation below belongs to the path under test.
func readyFilter() types.WorkFilter {
	return types.WorkFilter{IncludeDeferred: true}
}

// TestCountsBrokenWispPlaneIsAnError is the counts/ready twin of
// TestSearchInTxBrokenWispPlaneIsAnError: a wisp query that failed because a
// table it reads but the wisp plane does not own has gone missing was reported
// as "no wisps", so `bd count`, `bd count --by-*`, `bd search --counts` and
// `bd ready` answered a durable-only number over a broken database and told
// the caller nothing.
//
// The assertion is errors.Is against the primed driver error rather than a
// substring of the message: the query text names the joined tables, so a
// substring check would pass on any error that merely echoes the query.
func TestCountsBrokenWispPlaneIsAnError(t *testing.T) {
	for _, tc := range countsEntryPoints {
		for _, missing := range tc.missing {
			t.Run(tc.name+"/"+missing, func(t *testing.T) {
				_, mock, tx := beginMockTx(t)
				gone := tableNotFound(missing)
				tc.prime(mock, gone)

				got, err := tc.run(context.Background(), tx)
				if err == nil {
					t.Fatalf("%s succeeded with no %s table, answering %s", tc.name, missing, got)
				}
				if !errors.Is(err, gone) {
					t.Fatalf("%s: error is not the missing-%s failure: %v", tc.name, missing, err)
				}
			})
		}
	}
}

// TestCountsMissingWispPlaneIsNotAnError is the control the tightened guard
// must keep green: a pre-migration database has no wisp plane at all, and a
// count over it really does have no wisps to add. Over-tightening to "never
// tolerate" passes every must-error assertion above and fails here.
func TestCountsMissingWispPlaneIsNotAnError(t *testing.T) {
	for _, tc := range countsEntryPoints {
		for _, table := range []string{"wisps", "wisp_dependencies"} {
			t.Run(tc.name+"/"+table, func(t *testing.T) {
				_, mock, tx := beginMockTx(t)
				tc.prime(mock, tableNotFound(table))

				got, err := tc.run(context.Background(), tx)
				if err != nil {
					t.Fatalf("%s errored on a database with no %s: %v", tc.name, table, err)
				}
				if got != tc.wantTolerated {
					t.Fatalf("%s answered %s, want %s", tc.name, got, tc.wantTolerated)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("%s: unmet sql expectations: %v", tc.name, err)
				}
			})
		}
	}
}

// TestCountsUnrelatedErrorPropagates is the second control: the tolerance must
// not be a blanket catch in either direction. A deadlock or a dropped
// connection was never a missing table and must still reach the caller.
func TestCountsUnrelatedErrorPropagates(t *testing.T) {
	for _, tc := range countsEntryPoints {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			boom := errors.New("connection refused")
			tc.prime(mock, boom)

			if _, err := tc.run(context.Background(), tx); !errors.Is(err, boom) {
				t.Fatalf("%s did not propagate a failed wisp query: %v", tc.name, err)
			}
		})
	}
}

// TestCountsHealthyPlaneStillMerges is the GH#4387 parity control: on a
// database whose wisp plane is intact, both legs answer and the merged number
// is unchanged by the tightening.
func TestCountsHealthyPlaneStillMerges(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM issues`).WillReturnRows(scalarCountRows(3))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM wisps`).WillReturnRows(scalarCountRows(2))

	n, err := CountIssuesInTx(context.Background(), tx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("CountIssuesInTx over a healthy database: %v", err)
	}
	if n != 5 {
		t.Fatalf("CountIssuesInTx = %d, want 5 (3 issues + 2 wisps)", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
