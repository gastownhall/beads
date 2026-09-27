package fix

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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
