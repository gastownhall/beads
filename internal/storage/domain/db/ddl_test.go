package db

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func newMockDDL(t *testing.T) (sqlmock.Sqlmock, DDLSQLRepository) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return mock, NewDDLSQLRepository(db)
}

var schemataProbe = regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?")

func TestDatabaseExistsAsksTheServerByParameter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		want  bool
	}{
		{"present", 1, true},
		{"absent", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, ddl := newMockDDL(t)
			mock.ExpectQuery(schemataProbe).
				WithArgs("Beads_Vulcan").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.count))

			got, err := ddl.DatabaseExists(t.Context(), "Beads_Vulcan")
			if err != nil {
				t.Fatalf("DatabaseExists: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DatabaseExists = %v, want %v", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDatabaseExistsReturnsProbeError(t *testing.T) {
	mock, ddl := newMockDDL(t)
	boom := errors.New("boom")
	mock.ExpectQuery(schemataProbe).WithArgs("beads").WillReturnError(boom)

	if _, err := ddl.DatabaseExists(t.Context(), "beads"); !errors.Is(err, boom) {
		t.Fatalf("DatabaseExists error = %v, want %v", err, boom)
	}
}

func (s *testSuite) TestDatabaseExistsMatchesTheServerCaseRule() {
	ddl := NewDDLSQLRepository(s.db)
	for _, name := range []string{s.dbName, strings.ToUpper(s.dbName)} {
		exists, err := ddl.DatabaseExists(s.Ctx(), name)
		s.Require().NoError(err)
		s.Require().True(exists, "DatabaseExists(%q) on a server that stores %q", name, s.dbName)
	}
	exists, err := ddl.DatabaseExists(s.Ctx(), s.dbName+"_absent")
	s.Require().NoError(err)
	s.Require().False(exists)
}
