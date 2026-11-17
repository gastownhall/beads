package schema

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
)

// backfillProbeSQL is the one statement migrationWorkNeeded sends to decide
// whether custom_types or custom_statuses still needs a backfill from config:
// both table counts and both config values. Spelled out here rather than taken
// from the production constant, so a change to the statement's shape is a
// visible test change.
const backfillProbeSQL = "SELECT " +
	"(SELECT COUNT(*) FROM custom_types), " +
	"(SELECT `value` FROM config WHERE `key` = 'types.custom'), " +
	"(SELECT COUNT(*) FROM custom_statuses), " +
	"(SELECT `value` FROM config WHERE `key` = 'status.custom')"

// expectBackfillProbe mocks the one-statement custom-table backfill probe. A
// nil value stands for an absent config row (the scalar subquery reads NULL).
func expectBackfillProbe(mock sqlmock.Sqlmock, typesCount int, typesValue any, statusesCount int, statusesValue any) {
	mock.ExpectQuery(regexp.QuoteMeta(backfillProbeSQL)).
		WillReturnRows(sqlmock.NewRows([]string{"types_count", "types_value", "statuses_count", "statuses_value"}).
			AddRow(typesCount, typesValue, statusesCount, statusesValue))
}

// expectCustomTablesPopulated mocks the backfill probe on a database whose
// custom tables both hold rows, so nothing needs a backfill.
func expectCustomTablesPopulated(mock sqlmock.Sqlmock) {
	expectBackfillProbe(mock, 1, "bug,feature", 1, "review:wip")
}

// TestNeedsBackfillOneStatement pins that the custom-table backfill decision
// costs one statement whatever the tables and config hold: empty tables with
// usable config, empty tables with no config, populated tables, and empty
// tables whose config is empty or malformed (which fall back to "no backfill",
// as before). sqlmock matches in order and fails on any statement it was not
// told to expect, so a second read fails the test.
func TestNeedsBackfillOneStatement(t *testing.T) {
	cases := []struct {
		name          string
		typesCount    int
		typesValue    any
		statusesCount int
		statusesValue any
		want          bool
	}{
		{"empty tables, both configs usable", 0, "bug,feature", 0, "review:wip", true},
		{"empty tables, only types config", 0, "bug,feature", 0, nil, true},
		{"empty tables, only statuses config", 0, nil, 0, "review:wip", true},
		{"empty tables, no config rows", 0, nil, 0, nil, false},
		{"populated tables", 3, "bug,feature", 2, "review:wip", false},
		{"populated tables, no config rows", 3, nil, 2, nil, false},
		{"types populated, statuses empty with usable config", 3, "bug", 0, "review:wip", true},
		{"empty tables, empty config values", 0, "", 0, "", false},
		{"empty tables, blank config values", 0, " , ", 0, "   ", false},
		{"empty tables, malformed statuses config", 0, nil, 0, "review:bogus", false},
		{"empty tables, statuses config colliding with a built-in", 0, nil, 0, "open", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			expectBackfillProbe(mock, c.typesCount, c.typesValue, c.statusesCount, c.statusesValue)

			got, err := needsBackfilledCustomStatusesCustomTypes(context.Background(), db)
			if err != nil {
				t.Fatalf("needsBackfilledCustomStatusesCustomTypes: %v", err)
			}
			if got != c.want {
				t.Errorf("needs backfill = %v, want %v", got, c.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// TestNeedsBackfillProbeFailsClosed pins that an error from the one-statement
// probe that is not a missing table is returned, not read as "no backfill".
func TestNeedsBackfillProbeFailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	boom := errors.New("connection reset")
	mock.ExpectQuery(regexp.QuoteMeta(backfillProbeSQL)).WillReturnError(boom)

	got, err := needsBackfilledCustomStatusesCustomTypes(context.Background(), db)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if got {
		t.Error("needs backfill = true on a failed probe, want false with the error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestNeedsBackfillMissingTableKeepsSeparateProbeAnswer covers a database
// whose cursor says config (main migration 0006) and the custom tables (main
// migration 0024) exist while one of them is gone. One statement cannot say
// which table is missing, so the probe falls back to the separate reads and
// gives the answer they always gave: a missing custom table is an error, and a
// missing config table is not read at all when both custom tables hold rows.
func TestNeedsBackfillMissingTableKeepsSeparateProbeAnswer(t *testing.T) {
	missing := func(table string) error {
		return &mysql.MySQLError{Number: 1146, Message: "Table 'beads." + table + "' doesn't exist"}
	}

	t.Run("missing config with populated custom tables needs nothing", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta(backfillProbeSQL)).WillReturnError(missing("config"))
		expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
		expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)

		got, err := needsBackfilledCustomStatusesCustomTypes(context.Background(), db)
		if err != nil || got {
			t.Fatalf("needs backfill = %v, %v; want false, nil", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("missing custom_types is an error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta(backfillProbeSQL)).WillReturnError(missing("custom_types"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM custom_types")).WillReturnError(missing("custom_types"))

		_, err = needsBackfilledCustomStatusesCustomTypes(context.Background(), db)
		if err == nil || !strings.Contains(err.Error(), "custom_types") {
			t.Fatalf("err = %v, want the custom_types read error", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}
