package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrationWorkNeededWhenRecordedDDLIsAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectIgnoredSentinelProbes(mock, true)
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectNoIDDefaultInvariantRepairSentinel(mock)
	expectIDDefaultInvariants(mock, mainIDDefaultInvariantTables, map[string]string{"dependencies": "uuid()"})

	needed, err := migrationWorkNeeded(context.Background(), db)
	if err != nil {
		t.Fatalf("migrationWorkNeeded: %v", err)
	}
	if !needed {
		t.Fatal("migrationWorkNeeded = false when v50 is recorded but dependencies.id still has DEFAULT (uuid())")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRepairIDDefaultInvariantsDropsAndVerifiesDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectIDDefaultInvariants(mock, mainIDDefaultInvariantTables, map[string]string{"dependencies": "uuid()"})
	mock.ExpectExec(regexp.QuoteMeta("ALTER TABLE `dependencies` ALTER COLUMN id DROP DEFAULT")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectHealthyIDDefaultInvariants(mock, mainIDDefaultInvariantTables)

	changed, err := repairIDDefaultInvariants(context.Background(), db, mainIDDefaultInvariantTables)
	if err != nil {
		t.Fatalf("repairIDDefaultInvariants: %v", err)
	}
	if len(changed) != 1 || changed[0] != "dependencies" {
		t.Fatalf("repairIDDefaultInvariants changed = %v, want [dependencies]", changed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCommitIDDefaultInvariantRepairsResumesAfterInterruptedAlter(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("FROM dolt_diff_summary('HEAD', 'WORKING')")).
		WillReturnRows(sqlmock.NewRows([]string{"to_table_name", "data_change"}).AddRow("dependencies", false))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT from_create_statement, to_create_statement FROM dolt_schema_diff('HEAD', 'WORKING', ?)")).
		WithArgs("dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"from_create_statement", "to_create_statement"}).
			AddRow("CREATE TABLE dependencies (id char(36) DEFAULT (uuid()))", "CREATE TABLE dependencies (id char(36))"))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs("dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs("schema: repair recorded migration invariants").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	expectDoltStatusRows(mock)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM local_metadata WHERE `key` = ?")).
		WithArgs(idDefaultInvariantRepairSentinel).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := commitIDDefaultInvariantRepairs(context.Background(), db, nil); err != nil {
		t.Fatalf("commitIDDefaultInvariantRepairs: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRunMigrationsDoesNotRecordVersionWhenInvariantCannotBeApplied(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	originalCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	defer func() { issueRowCounter = originalCounter }()

	mock.ExpectExec("(?s).*Migration 0050.*").WillReturnResult(sqlmock.NewResult(0, 0))
	expectIDDefaultInvariants(mock, []string{"dependencies"}, map[string]string{"dependencies": "uuid()"})
	applyErr := errors.New("prepared ALTER did not take effect")
	mock.ExpectExec(regexp.QuoteMeta("ALTER TABLE `dependencies` ALTER COLUMN id DROP DEFAULT")).
		WillReturnError(applyErr)

	applied, err := runMigrations(context.Background(), db, mainSource, 49, 50, false)
	if applied != 0 {
		t.Fatalf("runMigrations applied = %d, want 0", applied)
	}
	if !errors.Is(err, applyErr) {
		t.Fatalf("runMigrations error = %v, want invariant error", err)
	}
	if !strings.Contains(err.Error(), "verifying migration 0050_dependencies_deterministic_id.up.sql") {
		t.Fatalf("runMigrations error lacks migration context: %v", err)
	}
	// No schema_migrations INSERT is expected. sqlmock fails if the runner
	// records v50 after the invariant repair errors.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRunMigrationsDoesNotRecordVersionWhenDDLStillContradictsMark(t *testing.T) {
	originalCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	defer func() { issueRowCounter = originalCounter }()

	cases := []struct {
		name          string
		source        migrationSource
		minVersion    int
		upTo          int
		migrationName string
		tables        []string
		stubbornTable string
	}{
		{"main 0050", mainSource, 49, 50, "0050_dependencies_deterministic_id.up.sql", mainIDDefaultInvariantTables[:1], "dependencies"},
		{"main 0051", mainSource, 50, 51, "0051_drop_aux_id_defaults.up.sql", mainIDDefaultInvariantTables[1:], "events"},
		{"ignored 0010", ignoredSource, 9, 10, "0010_drop_wisp_id_defaults.up.sql", ignoredIDDefaultInvariantTables, "wisp_events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectExec("(?s).*").WillReturnResult(sqlmock.NewResult(0, 0))
			expectIDDefaultInvariants(mock, tc.tables, map[string]string{tc.stubbornTable: "uuid()"})
			mock.ExpectExec(regexp.QuoteMeta("ALTER TABLE `" + tc.stubbornTable + "` ALTER COLUMN id DROP DEFAULT")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			// Model GH#5269's exact silent-success mode: ALTER reports success,
			// but the live DDL remains unchanged.
			expectIDDefaultInvariants(mock, tc.tables, map[string]string{tc.stubbornTable: "uuid()"})

			applied, err := runMigrations(context.Background(), db, tc.source, tc.minVersion, tc.upTo, false)
			if applied != 0 {
				t.Fatalf("runMigrations applied = %d, want 0", applied)
			}
			if err == nil || !strings.Contains(err.Error(), "migration invariant verification failed") {
				t.Fatalf("runMigrations error = %v, want invariant verification failure", err)
			}
			if !strings.Contains(err.Error(), tc.migrationName) {
				t.Fatalf("runMigrations error lacks migration name %s: %v", tc.migrationName, err)
			}
			// No cursor INSERT is expected; sqlmock rejects one.
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func expectIDDefaultInvariants(mock sqlmock.Sqlmock, tables []string, defaults map[string]string) {
	args := make([]driver.Value, len(tables))
	rows := sqlmock.NewRows([]string{"TABLE_NAME", "COLUMN_DEFAULT"})
	for i, table := range tables {
		args[i] = table
		if columnDefault, ok := defaults[table]; ok {
			rows.AddRow(table, columnDefault)
		} else {
			rows.AddRow(table, nil)
		}
	}
	mock.ExpectQuery(`(?s)SELECT TABLE_NAME, COLUMN_DEFAULT.*FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME IN`).
		WithArgs(args...).
		WillReturnRows(rows)
}
