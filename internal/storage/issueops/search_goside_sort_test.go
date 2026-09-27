package issueops

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// beginCapturingMockTx is beginMockTx with a query matcher that also records
// every actual SQL string, so a test can assert about clause ABSENCE — the
// default regexp matcher can only prove presence, and Go regexps have no
// negative lookahead to express "carries no LIMIT".
func beginCapturingMockTx(t *testing.T, captured *[]string) (sqlmock.Sqlmock, DBTX) {
	t.Helper()

	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		*captured = append(*captured, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	return mock, tx
}

// TestSearchIssueIDsInTx_GoSideSortScansUnboundedThenOrdersAndTrims is the
// store-backed half of bd-jao3t: a Go-side sort key ("id") renders no ORDER
// BY, and a LIMIT with no ORDER BY returns n rows, not the first n — so the
// leg must scan the complete matching set, order it in Go, and only then
// apply the bound (goSideSortAndTrim). The mock returns the matching set
// deliberately out of order; the delivered page must be the true byte-order
// top-3, honoring sortDesc — before the fix it was an arbitrary LIMIT-cut
// subset in whatever order the engine emitted, and sortDesc was ignored for
// this key besides (sqlbuild.Less).
func TestSearchIssueIDsInTx_GoSideSortScansUnboundedThenOrdersAndTrims(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		sortDesc bool
		want     []string
	}{
		{"ascending", false, []string{"bd-000", "bd-001", "bd-003"}},
		{"descending", true, []string{"bd-009", "bd-007", "bd-004"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var captured []string
			mock, tx := beginCapturingMockTx(t, &captured)

			// The complete matching set, in an order no sort would produce.
			mock.ExpectQuery(`SELECT issues\.id FROM issues`).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).
					AddRow("bd-004").AddRow("bd-001").AddRow("bd-009").
					AddRow("bd-003").AddRow("bd-000").AddRow("bd-007"))

			got, err := SearchIssueIDsInTx(context.Background(), tx, "", types.IssueFilter{
				SortBy:    "id",
				SortDesc:  tc.sortDesc,
				Limit:     3,
				SkipWisps: true,
			})
			if err != nil {
				t.Fatalf("SearchIssueIDsInTx: %v", err)
			}

			if len(got) != len(tc.want) || got[0] != tc.want[0] || got[1] != tc.want[1] || got[2] != tc.want[2] {
				t.Fatalf("page = %v, want %v (byte-order top-3, sortDesc=%v)", got, tc.want, tc.sortDesc)
			}

			for _, q := range captured {
				if strings.Contains(q, "LIMIT") {
					t.Fatalf("a Go-side sort leg must not carry a LIMIT (a bound without an order is n rows, not the first n); got: %s", q)
				}
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestSearchIssuesWithCountsInTx_GoSideSortDropsTheLegBound is the counts-seam
// copy of the leg-shape assertion — the seam bd query '<expr>' --sort id
// actually reaches on the store-shaped backends (storequerier →
// SearchIssuesWithCounts), where BuildQueryPlan always pushes a bound. Under a
// Go-side sort, neither the issues nor the wisps counts query may carry a
// LIMIT (no ORDER BY is rendered for this key, and a bound without an order is
// n rows, not the first n); runFilterSearchQueryInTx orders and bounds the leg
// in Go instead (goSideSortAndTrim).
func TestSearchIssuesWithCountsInTx_GoSideSortDropsTheLegBound(t *testing.T) {
	t.Parallel()

	var captured []string
	mock, tx := beginCapturingMockTx(t, &captured)

	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM issues i`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM wisps i`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	txSQL, ok := tx.(*sql.Tx)
	if !ok {
		t.Fatal("beginCapturingMockTx must hand back a *sql.Tx for the counts seam")
	}
	if _, err := SearchIssuesWithCountsInTx(context.Background(), txSQL, "", types.IssueFilter{SortBy: "id", SortDesc: true, Limit: 3}); err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}

	for _, q := range captured {
		if strings.Contains(q, "FROM wisp") && strings.HasPrefix(q, "SELECT 1") {
			continue // the existence probes legitimately carry LIMIT 1
		}
		if strings.Contains(q, "LIMIT") {
			t.Fatalf("a Go-side-sort counts leg must not carry a LIMIT; got: %s", q)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestSearchIssueIDsInTx_SQLSortStillPushesTheBound guards the other
// direction: for a sort key SQL renders (the default priority order here),
// the leg keeps its ORDER BY and its LIMIT — the bd-jao3t fix widens only
// the one key SQL cannot order.
func TestSearchIssueIDsInTx_SQLSortStillPushesTheBound(t *testing.T) {
	t.Parallel()

	var captured []string
	mock, tx := beginCapturingMockTx(t, &captured)

	mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY.*LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow("bd-000").AddRow("bd-001").AddRow("bd-002"))

	got, err := SearchIssueIDsInTx(context.Background(), tx, "", types.IssueFilter{
		Limit:     3,
		SkipWisps: true,
	})
	if err != nil {
		t.Fatalf("SearchIssueIDsInTx: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("page length = %d, want 3", len(got))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
