package fix

import (
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// probeForCorrectDoltDatabase interpolates candidate database names into a
// backtick-quoted query. Names that could break out of that quoting (backticks,
// whitespace, dashes, semicolons) must be skipped before any query is built, so
// a hostile database on a shared server can't steer the probe.
func TestProbeForCorrectDoltDatabaseSkipsUnsafeNames(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	databases := sqlmock.NewRows([]string{"Database"}).
		AddRow("x`; DROP TABLE issues; --").
		AddRow("good_db")
	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(databases)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `good_db`.issues LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(3))

	if got := probeForCorrectDoltDatabase(db, ""); got != "good_db" {
		t.Fatalf("probeForCorrectDoltDatabase = %q, want %q", got, "good_db")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected queries: %v", err)
	}
}

func TestDoltIdentifierPattern(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"beads", true},
		{"beads_prod", true},
		{"db$money", true},
		{"", false},
		{"bad name", false},
		{"back`tick", false},
		{"semi;colon", false},
		{"dash-name", false},
		{"quote'name", false},
	}
	for _, tc := range cases {
		if got := doltIdentifierPattern.MatchString(tc.name); got != tc.ok {
			t.Errorf("doltIdentifierPattern.MatchString(%q) = %v, want %v", tc.name, got, tc.ok)
		}
	}
}
