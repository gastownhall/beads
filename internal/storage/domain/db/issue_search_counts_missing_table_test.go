package db

import (
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// unionCountsOverBrokenWispPlane drives the counted union to the point where
// it hydrates the wisp rows, and fails that hydration with hydrateErr.
//
// The union leg is not where the breakage shows. Its FROM is plan.FromSQL,
// which carries no lease join and reads neither labels nor comments, so the
// union query itself succeeds on a database missing `leases`, `wisp_labels`
// or `wisp_comments` and returns wisp ids. Only fetchCountsByIDs renders the
// counts mega-query -- which adds sqlbuild.LeaseJoin, hydrates labels, and
// LEFT JOINs the comment-count subquery (sqlbuild/counts.go:198) -- so it is
// the first query to see the breakage, and tolerating it drops every wisp
// from the counted page behind a nil error. This is the counts twin of
// unionOverBrokenWispPlane in issue_search_missing_table_test.go.
func unionCountsOverBrokenWispPlane(t *testing.T, hydrateErr error) (sqlmock.Sqlmock, domain.SearchCountsPage, error) {
	t.Helper()
	mock, repo := newMockRepo(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("UNION ALL").WillReturnRows(
		sqlmock.NewRows([]string{"id", "src"}).AddRow("bd-w1", "w"))
	mock.ExpectQuery(`(?s)FROM wisps`).WillReturnError(hydrateErr)

	page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "", types.IssueFilter{})
	return mock, page, err
}

// TestSearchCountsUnionBrokenWispPlaneIsAnError is the counted-page twin of
// TestSearchUnionBrokenWispPlaneIsAnError. `bd search --counts` and every
// store-shaped caller that projects counts ran through this path, and a wisp
// hydration that failed because a table the wisp plane does not own had gone
// missing was answered as "there are no wisps".
//
// The assertion is errors.Is against the primed driver error, not a substring
// of the message: the query text names the joined tables, so a substring check
// would pass on any error that merely echoes the query.
func TestSearchCountsUnionBrokenWispPlaneIsAnError(t *testing.T) {
	for _, missing := range []string{"wisp_labels", "wisp_comments", "leases"} {
		t.Run(missing, func(t *testing.T) {
			gone := missingTable(missing)
			_, page, err := unionCountsOverBrokenWispPlane(t, gone)
			if err == nil {
				t.Fatalf("counted search hid a broken wisp plane, returning %d rows", len(page.Items))
			}
			if !errors.Is(err, gone) {
				t.Fatalf("error is not the missing-%s failure: %v", missing, err)
			}
		})
	}
}

// TestSearchCountsUnionMissingWispPlaneIsNotAnError is the control the
// tightened guard must keep: the wisp plane's own tables are the ones a
// database may legitimately not have. Over-tightening to "never tolerate"
// passes the assertions above and fails here.
func TestSearchCountsUnionMissingWispPlaneIsNotAnError(t *testing.T) {
	for _, table := range []string{"wisps", "wisp_dependencies"} {
		t.Run(table, func(t *testing.T) {
			mock, page, err := unionCountsOverBrokenWispPlane(t, missingTable(table))
			if err != nil {
				t.Fatalf("counted search errored on a database with no %s: %v", table, err)
			}
			if len(page.Items) != 0 {
				t.Fatalf("got %d rows, want 0", len(page.Items))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestSearchCountsUnionUnrelatedErrorPropagates is the second control: the
// tolerance must not be a blanket catch in either direction.
func TestSearchCountsUnionUnrelatedErrorPropagates(t *testing.T) {
	boom := errors.New("connection refused")
	if _, _, err := unionCountsOverBrokenWispPlane(t, boom); !errors.Is(err, boom) {
		t.Fatalf("counted search did not propagate a failed hydration: %v", err)
	}
}
