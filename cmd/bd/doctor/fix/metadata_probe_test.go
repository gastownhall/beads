package fix

import (
	"database/sql"
	"errors"
	"reflect"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestProbeForCorrectDoltDatabaseQuotesIdentifier pins the call site, not just
// the escape helper. probeForCorrectDoltDatabase is the `bd doctor --fix` half
// of the GH#2160 wrong-database repair; its twin in package doctor
// (probeForCorrectDatabase) emits the "run bd doctor --fix" instruction. While
// this probe interpolated the SHOW DATABASES name raw, a backtick-bearing name
// made the SELECT error, so the repair returned "" and did nothing at all —
// no fix, no error, no output — for exactly the names the check had just told
// the user to repair. Expectations are exact strings, so reverting to raw
// interpolation fails this test rather than silently regressing.
func TestProbeForCorrectDoltDatabaseQuotesIdentifier(t *testing.T) {
	tests := []struct {
		name      string
		dbName    string
		wantQuery string
	}{
		{
			name:      "plain name is unchanged",
			dbName:    "beads_x",
			wantQuery: "SELECT COUNT(*) FROM `beads_x`.issues LIMIT 1",
		},
		{
			name:      "backtick cannot break out of the identifier",
			dbName:    "evil`; DROP TABLE x",
			wantQuery: "SELECT COUNT(*) FROM `evil``; DROP TABLE x`.issues LIMIT 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectQuery("SHOW DATABASES").
				WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow(tt.dbName))
			mock.ExpectQuery(regexp.QuoteMeta(tt.wantQuery)).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

			if got := probeForCorrectDoltDatabase(db, "beads"); got != tt.dbName {
				t.Errorf("probeForCorrectDoltDatabase() = %q, want %q", got, tt.dbName)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

// newRecordingMock wraps sqlmock with a matcher that appends each actual query
// to a recorded list before delegating to the default regexp matcher. The
// recorded list pins the ordered set of queries that reached expectation
// matching; it does not capture queries sqlmock rejects before matching (an
// unexpected query surfaces as an error the probe's `err == nil` path
// swallows), so the assertions pair DeepEqual on the recording with
// ExpectationsWereMet, which together bound mid-sequence drift; a query issued
// after the last expectation is fulfilled is caught by neither. In ordered mode
// sqlmock's query() skips fulfilled expectations and returns "all expectations
// were already fulfilled" before it reaches the matcher, so a trailing surplus
// query is never recorded, and ExpectationsWereMet reports only unfulfilled
// expectations, never surplus calls. sqlmock v1.5.2 offers no clean trailing
// sentinel: an extra never-matching expectation would fail
// ExpectationsWereMet unconditionally.
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

// The winner is a neutral name rather than configfile.DefaultDoltDatabase: the
// sole production caller passes that default as skipDB (metadata.go:232), so it
// is filtered on every real call and can never be the returned winner. This
// test passes it as skipDB with a matching SHOW DATABASES row, which both keeps
// the production argument shape and makes the filter observable — no expectation
// is registered for it, so the recorded list below is the proof it was never
// probed.
func TestProbeForCorrectDoltDatabasePrefersFirstProbeableCandidate(t *testing.T) {
	db, mock, recorded := newRecordingMock(t)

	databases := sqlmock.NewRows([]string{"Database"}).
		AddRow(configfile.DefaultDoltDatabase).
		AddRow("stale-db").
		AddRow("project-db")
	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(databases)
	// The first surviving candidate's probe fails (no issues table): it is
	// probed, not skipped, and the scan continues to the next candidate.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `stale-db`.issues LIMIT 1")).
		WillReturnError(errors.New("table not found"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `project-db`.issues LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(1))

	if got := probeForCorrectDoltDatabase(db, configfile.DefaultDoltDatabase); got != "project-db" {
		t.Fatalf("probeForCorrectDoltDatabase = %q, want %q", got, "project-db")
	}

	want := []string{
		"SHOW DATABASES",
		"SELECT COUNT(*) FROM `stale-db`.issues LIMIT 1",
		"SELECT COUNT(*) FROM `project-db`.issues LIMIT 1",
	}
	if !reflect.DeepEqual(*recorded, want) {
		t.Fatalf("executed queries = %q, want %q", *recorded, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected queries: %v", err)
	}
}
