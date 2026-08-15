package db

import (
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

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
