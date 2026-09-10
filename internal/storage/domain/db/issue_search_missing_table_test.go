package db

import (
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func missingTable(table string) error {
	return &mysql.MySQLError{Number: 1146, Message: "table not found: " + table}
}

func ephemeralFilter() types.IssueFilter {
	on := true
	return types.IssueFilter{Ephemeral: &on}
}

func newMockRepo(t *testing.T) (sqlmock.Sqlmock, *issueSQLRepositoryImpl) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return mock, &issueSQLRepositoryImpl{runner: db}
}

// TestSearchAcrossEphemeralBrokenWispPlaneIsAnError mirrors the issueops guard
// on the domain/db stack: the two must agree on what a missing table means, or
// the same query answers differently depending on which stack served it.
func TestSearchAcrossEphemeralBrokenWispPlaneIsAnError(t *testing.T) {
	for _, missing := range []string{"wisp_labels", "leases"} {
		t.Run(missing, func(t *testing.T) {
			mock, repo := newMockRepo(t)
			gone := missingTable(missing)
			mock.ExpectQuery("FROM wisps").WillReturnError(gone)
			mock.ExpectQuery("SELECT 1 FROM wisps").WillReturnError(missingTable("wisps"))
			mock.ExpectQuery("FROM issues").WillReturnRows(sqlmock.NewRows([]string{"id"}))

			_, err := repo.searchAcrossIssuesAndWisps(t.Context(), "", ephemeralFilter())
			if !errors.Is(err, gone) {
				t.Fatalf("search hid a broken wisp plane: %v", err)
			}
		})
	}
}

// TestSearchAcrossEphemeralMissingWispPlaneIsEmpty is the control: a
// pre-migration database has no wisp plane, and searching it for wisps
// really does match nothing.
func TestSearchAcrossEphemeralMissingWispPlaneIsEmpty(t *testing.T) {
	for _, table := range []string{"wisps", "wisp_dependencies"} {
		t.Run(table, func(t *testing.T) {
			mock, repo := newMockRepo(t)
			mock.ExpectQuery("FROM wisps").WillReturnError(missingTable(table))
			mock.ExpectQuery("SELECT 1 FROM wisps").WillReturnError(missingTable("wisps"))
			mock.ExpectQuery("FROM issues").WillReturnRows(sqlmock.NewRows([]string{"id"}))

			page, err := repo.searchAcrossIssuesAndWisps(t.Context(), "", ephemeralFilter())
			if err != nil {
				t.Fatalf("search errored on a database with no wisp plane: %v", err)
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

// TestSearchAcrossEphemeralUnrelatedErrorPropagates is the second control, the
// one that pins the tolerance from the other side: a guard that fails open on
// any error it cannot parse a table name out of would still pass every test
// above.
func TestSearchAcrossEphemeralUnrelatedErrorPropagates(t *testing.T) {
	mock, repo := newMockRepo(t)
	boom := errors.New("connection refused")
	mock.ExpectQuery("FROM wisps").WillReturnError(boom)

	_, err := repo.searchAcrossIssuesAndWisps(t.Context(), "", ephemeralFilter())
	if !errors.Is(err, boom) {
		t.Fatalf("search did not propagate a failed wisps query: %v", err)
	}
}

// unionOverBrokenWispPlane primes the merged UNION path up to the point where
// it hydrates the wisp rows, then fails that hydration with hydrateErr.
//
// The union leg is where the two stacks diverge. Its FROM clause is
// plan.FromSQL, which carries no lease join and reads no labels, so the union
// query itself succeeds against a database missing `leases` or `wisp_labels`
// and returns wisp ids. Only fetchIssuesByIDs adds sqlbuild.LeaseJoin and
// hydrates labels, so that is the first query to see the breakage -- and
// tolerating it there drops every wisp from a merged search behind a nil
// error, which is the exact silent-row-loss this guard exists to stop.
func unionOverBrokenWispPlane(t *testing.T, hydrateErr error) (sqlmock.Sqlmock, domain.SearchPage, error) {
	t.Helper()
	mock, repo := newMockRepo(t)
	mock.ExpectQuery("SELECT 1 FROM wisps").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("UNION ALL").WillReturnRows(
		sqlmock.NewRows([]string{"id", "src"}).AddRow("bd-w1", "w"))
	mock.ExpectQuery("FROM wisps").WillReturnError(hydrateErr)

	page, err := repo.searchAcrossIssuesAndWisps(t.Context(), "", types.IssueFilter{})
	return mock, page, err
}

// TestSearchUnionBrokenWispPlaneIsAnError is the merged-path twin of
// TestSearchAcrossEphemeralBrokenWispPlaneIsAnError. The ephemeral branch is
// reachable only with an explicit filter; the union is what an ordinary
// `bd list` runs, so this is the guard most searches actually depend on.
func TestSearchUnionBrokenWispPlaneIsAnError(t *testing.T) {
	for _, missing := range []string{"wisp_labels", "leases"} {
		t.Run(missing, func(t *testing.T) {
			gone := missingTable(missing)
			_, _, err := unionOverBrokenWispPlane(t, gone)
			if !errors.Is(err, gone) {
				t.Fatalf("merged search hid a broken wisp plane: %v", err)
			}
		})
	}
}

// TestSearchUnionMissingWispPlaneIsEmpty is the control for the union path: a
// table the wisp plane genuinely owns can be absent, and the merged search
// still answers -- one row short, without an error.
func TestSearchUnionMissingWispPlaneIsEmpty(t *testing.T) {
	for _, table := range []string{"wisps", "wisp_dependencies"} {
		t.Run(table, func(t *testing.T) {
			mock, page, err := unionOverBrokenWispPlane(t, missingTable(table))
			if err != nil {
				t.Fatalf("merged search errored on a database with no wisp plane: %v", err)
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

// TestSearchUnionUnrelatedErrorPropagates is the union path's fail-open
// control.
func TestSearchUnionUnrelatedErrorPropagates(t *testing.T) {
	boom := errors.New("connection refused")
	_, _, err := unionOverBrokenWispPlane(t, boom)
	if !errors.Is(err, boom) {
		t.Fatalf("merged search did not propagate a failed hydration: %v", err)
	}
}
