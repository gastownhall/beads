package fix

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
)

// newRecordingMock wraps sqlmock with a matcher that appends each actual query
// to a recorded list before delegating to the default regexp matcher. The
// recorded list pins the ordered set of queries that reached expectation
// matching; paired with ExpectationsWereMet it bounds mid-sequence drift. In
// ordered mode sqlmock returns "all expectations were already fulfilled"
// before it reaches the matcher, so a trailing surplus query is never
// recorded; the reader turns that error into a returned error, which the
// tests below would see.
func newRecordingMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	var recorded []string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(
		func(expectedSQL, actualSQL string) error {
			recorded = append(recorded, actualSQL)
			return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
		},
	)))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock, &recorded
}

var errMissingTable = &mysql.MySQLError{Number: 1146, Message: "table not found"}

// TestReadServerMetadataDatabasesQuotesIdentifiers pins the catalog read the
// FixMissingDoltDatabase fallback selects from (it replaced the first-match
// probeForCorrectDoltDatabase, GH#5576). Two properties hold on both the
// issues and the _project_id query:
//
//  1. Every database name is backtick-doubled, the complete escape for a
//     backtick-quoted identifier, so a hostile name on a shared server stays
//     inert inside the quoting.
//  2. Legal names that are not unquoted-charset-safe read verbatim: legacy
//     databases were named from raw prefixes before GH#2142 (`my-project`,
//     hyphens included) and are what the GH#2160 repair looks for.
//
// Expectations are exact strings, so raw interpolation fails this test.
func TestReadServerMetadataDatabasesQuotesIdentifiers(t *testing.T) {
	db, mock, recorded := newRecordingMock(t)

	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(sqlmock.NewRows([]string{"Database"}).
		AddRow("information_schema").
		AddRow("x`; DROP TABLE issues; --").
		AddRow("my-project"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `x``; DROP TABLE issues; --`.issues LIMIT 1")).
		WillReturnError(errMissingTable)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `x``; DROP TABLE issues; --`.metadata WHERE `key` = '_project_id' LIMIT 1")).
		WillReturnError(errMissingTable)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `my-project`.issues LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(3))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `my-project`.metadata WHERE `key` = '_project_id' LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("proj-123"))

	got, err := readServerMetadataDatabases(context.Background(), db)
	if err != nil {
		t.Fatalf("readServerMetadataDatabases: %v", err)
	}
	want := []serverDatabaseMetadata{
		{Name: "x`; DROP TABLE issues; --"},
		{Name: "my-project", HasSchema: true, IssueCount: 3, ProjectID: "proj-123"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readServerMetadataDatabases = %+v, want %+v", got, want)
	}

	wantQueries := []string{
		"SHOW DATABASES",
		"SELECT COUNT(*) FROM `x``; DROP TABLE issues; --`.issues LIMIT 1",
		"SELECT value FROM `x``; DROP TABLE issues; --`.metadata WHERE `key` = '_project_id' LIMIT 1",
		"SELECT COUNT(*) FROM `my-project`.issues LIMIT 1",
		"SELECT value FROM `my-project`.metadata WHERE `key` = '_project_id' LIMIT 1",
	}
	if !reflect.DeepEqual(*recorded, wantQueries) {
		t.Fatalf("executed queries = %q, want %q", *recorded, wantQueries)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestReadServerMetadataDatabasesToleratesUnreadablePeers keeps GH#4931: a
// peer database this user may not read is listed with neither schema nor
// identity rather than aborting the read.
func TestReadServerMetadataDatabasesToleratesUnreadablePeers(t *testing.T) {
	db, mock, _ := newRecordingMock(t)

	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow("private_db"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `private_db`.issues LIMIT 1")).
		WillReturnError(&mysql.MySQLError{Number: 1142, Message: "Access denied for user to table 'issues'"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `private_db`.metadata WHERE `key` = '_project_id' LIMIT 1")).
		WillReturnError(&mysql.MySQLError{Number: 1044, Message: "Access denied for user to database"})

	got, err := readServerMetadataDatabases(context.Background(), db)
	if err != nil {
		t.Fatalf("readServerMetadataDatabases: %v", err)
	}
	if want := []serverDatabaseMetadata{{Name: "private_db"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("readServerMetadataDatabases = %+v, want %+v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestReadServerMetadataDatabasesPropagatesReadErrors pins that only the
// expected probe errors read as "absent": any other failure of the schema or
// identity query, or of the database listing itself, is returned, so the
// fallback skips instead of selecting from a partial catalog.
func TestReadServerMetadataDatabasesPropagatesReadErrors(t *testing.T) {
	connReset := errors.New("connection reset")
	tests := []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		want   string
	}{
		{
			name: "listing fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SHOW DATABASES").WillReturnError(connReset)
			},
			want: "connection reset",
		},
		{
			name: "listing row fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SHOW DATABASES").WillReturnRows(sqlmock.NewRows([]string{"Database"}).
					AddRow("a_db").RowError(0, connReset))
			},
			want: "connection reset",
		},
		{
			name: "schema query fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SHOW DATABASES").WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow("a_db"))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `a_db`.issues LIMIT 1")).WillReturnError(connReset)
			},
			want: `probing database "a_db" for schema: connection reset`,
		},
		{
			name: "identity query fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SHOW DATABASES").WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow("a_db"))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `a_db`.issues LIMIT 1")).
					WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `a_db`.metadata WHERE `key` = '_project_id' LIMIT 1")).
					WillReturnError(connReset)
			},
			want: `probing database "a_db" for project_id: connection reset`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, _ := newRecordingMock(t)
			tt.expect(mock)
			got, err := readServerMetadataDatabases(context.Background(), db)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("readServerMetadataDatabases = %+v, %v; want error containing %q", got, err, tt.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}
