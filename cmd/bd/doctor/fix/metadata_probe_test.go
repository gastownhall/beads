package fix

import (
	"database/sql"
	"errors"
	"reflect"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// newRecordingMock wraps sqlmock with a matcher that appends each actual query
// to a recorded list before delegating to the default regexp matcher. The
// recorded list pins the ordered set of queries that reached expectation
// matching; it does not capture queries sqlmock rejects before matching (an
// unexpected query surfaces as an error the probe's `err == nil` path
// swallows), so the assertions pair DeepEqual on the recording with
// ExpectationsWereMet rather than claiming either alone sees everything.
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
	db, mock, recorded := newRecordingMock(t)

	databases := sqlmock.NewRows([]string{"Database"}).AddRow("stale-db").AddRow("beads")
	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(databases)
	// The first candidate's probe fails (no issues table): it is probed, not
	// skipped, and the scan continues to the second candidate.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `stale-db`.issues LIMIT 1")).
		WillReturnError(errors.New("table not found"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `beads`.issues LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(1))

	if got := probeForCorrectDoltDatabase(db, ""); got != "beads" {
		t.Fatalf("probeForCorrectDoltDatabase = %q, want %q", got, "beads")
	}

	want := []string{
		"SHOW DATABASES",
		"SELECT COUNT(*) FROM `stale-db`.issues LIMIT 1",
		"SELECT COUNT(*) FROM `beads`.issues LIMIT 1",
	}
	if !reflect.DeepEqual(*recorded, want) {
		t.Fatalf("executed queries = %q, want %q", *recorded, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected queries: %v", err)
	}
}
