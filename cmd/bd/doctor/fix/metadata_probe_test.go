package fix

import (
	"database/sql"
	"errors"
	"reflect"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// newRecordingMock wraps sqlmock with a matcher that records every query the
// code actually issues, so assertions run against the full executed set —
// including queries whose (escaped) text is hostile-looking. sqlmock's default
// expectation failure path cannot pin this property on its own: an unexpected
// query surfaces as an error the probe is allowed to swallow, and
// ExpectationsWereMet only sees the queries that were expected.
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

// probeForCorrectDoltDatabase interpolates candidate database names into a
// backtick-quoted query. Two properties must hold together:
//
//  1. Every candidate is probed with backtick-doubling (the complete escape
//     for a backtick-quoted identifier), so a hostile name on a shared server
//     stays inert inside the quoting — the probe merely errors and the
//     candidate is skipped.
//  2. Legal names that are not unquoted-charset-safe still probe verbatim.
//     Legacy databases were named from raw prefixes before GH#2142
//     (`my-project`-style, hyphens included) and this probe is the doctor's
//     mechanism for finding them.
func TestProbeForCorrectDoltDatabaseEscapesHostileNames(t *testing.T) {
	db, mock, recorded := newRecordingMock(t)

	databases := sqlmock.NewRows([]string{"Database"}).
		AddRow("x`; DROP TABLE issues; --").
		AddRow("my-project")
	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(databases)

	escapedHostile := regexp.QuoteMeta("SELECT COUNT(*) FROM `x``; DROP TABLE issues; --`.issues LIMIT 1")
	mock.ExpectQuery(escapedHostile).WillReturnError(errors.New("table not found"))

	legacyHyphen := regexp.QuoteMeta("SELECT COUNT(*) FROM `my-project`.issues LIMIT 1")
	mock.ExpectQuery(legacyHyphen).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(3))

	if got := probeForCorrectDoltDatabase(db, ""); got != "my-project" {
		t.Fatalf("probeForCorrectDoltDatabase = %q, want %q", got, "my-project")
	}

	want := []string{
		"SHOW DATABASES",
		"SELECT COUNT(*) FROM `x``; DROP TABLE issues; --`.issues LIMIT 1",
		"SELECT COUNT(*) FROM `my-project`.issues LIMIT 1",
	}
	if !reflect.DeepEqual(*recorded, want) {
		t.Fatalf("executed queries = %q, want %q", *recorded, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected queries: %v", err)
	}
}

func TestProbeForCorrectDoltDatabasePrefersFirstProbeableCandidate(t *testing.T) {
	db, mock, _ := newRecordingMock(t)

	databases := sqlmock.NewRows([]string{"Database"}).AddRow("beads")
	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(databases)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `beads`.issues LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(1))

	if got := probeForCorrectDoltDatabase(db, ""); got != "beads" {
		t.Fatalf("probeForCorrectDoltDatabase = %q, want %q", got, "beads")
	}
}
