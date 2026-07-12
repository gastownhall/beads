package schema

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrationLockNameUsesRawNameWhenBounded(t *testing.T) {
	got := MigrationLockName("testdb_short")
	want := migrationLockPrefix + "testdb_short"
	if got != want {
		t.Fatalf("MigrationLockName() = %q, want %q", got, want)
	}
}

func TestMigrationLockNameHashesLongNames(t *testing.T) {
	dbName := strings.Repeat("a", 64)
	got := MigrationLockName(dbName)
	if len(got) > migrationLockNameMaxLength {
		t.Fatalf("MigrationLockName() length = %d, want <= %d", len(got), migrationLockNameMaxLength)
	}
	if got == migrationLockPrefix+dbName {
		t.Fatalf("MigrationLockName() used over-limit raw name %q", got)
	}
	if got != MigrationLockName(dbName) {
		t.Fatal("MigrationLockName() is not deterministic")
	}
}

func TestIsMigrationLockError(t *testing.T) {
	err := errors.Join(ErrMigrationLockUnavailable, errors.New("timeout"))
	if !IsMigrationLockError(err) {
		t.Fatal("IsMigrationLockError() = false, want true")
	}
}

func TestMigrateUpRunsWithoutAdvisoryLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	expectOnePendingMigration(t, mock)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUp() applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMigrateUpWithLockUsesDatabaseScopedLockOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	defer conn.Close()

	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectOnePendingMigration(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpSeedsIgnorePatternsWhenNoWorkNeeded is the regression guard for
// out-of-band-materialized databases: one whose migration cursors arrived
// at-latest WITHOUT executing the seeding migrations (out-of-band table
// copy/rename) reports no migration work, but MigrateUp must still re-assert
// the full canonical dolt_ignore pattern set before the short-circuit, or the
// copied database is never healed (1 pattern instead of 5, wisp churn in
// dolt_status, dirty-gate block on subsequent migrations).
func TestMigrateUpSeedsIgnorePatternsWhenNoWorkNeeded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	expectIgnorePatternSeed(mock)
	// migrationWorkNeeded: both cursors at latest, both content_hash columns
	// present, no custom backfill pending -> no work, MigrateUp short-circuits.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
	// The seed inserted rows and no migration pass follows to commit them, so
	// MigrateUp must commit the heal itself, scoped and labeled.
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUp() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (ignore-pattern seed must run before the no-work short-circuit and be committed when it changed rows): %v", err)
	}
}

// TestMigrateUpSkipsSeedCommitWhenNothingChanged is the negative counterpart:
// on a healthy database every INSERT IGNORE is a no-op (0 rows affected), so
// the no-work short-circuit must NOT stage or commit dolt_ignore — sqlmock
// fails the test on any unexpected DOLT_ADD/DOLT_COMMIT call.
func TestMigrateUpSkipsSeedCommitWhenNothingChanged(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	expectIgnorePatternSeedNoop(mock)
	// migrationWorkNeeded: no work, MigrateUp short-circuits.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUp() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (no-op seed must not trigger a scoped commit): %v", err)
	}
}

// expectIgnorePatternSeed mocks the unconditional dolt_ignore pattern seed
// MigrateUp runs before anything else, with every pattern actually inserted
// (RowsAffected=1: an under-seeded database).
func expectIgnorePatternSeed(mock sqlmock.Sqlmock) {
	for _, pattern := range doltIgnorePatterns {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

// expectIgnorePatternSeedNoop mocks the seed on a healthy database: every
// INSERT IGNORE hits an existing row (RowsAffected=0), nothing changes.
func expectIgnorePatternSeedNoop(mock sqlmock.Sqlmock) {
	for _, pattern := range doltIgnorePatterns {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func expectOnePendingMigration(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()

	latest := LatestVersion()
	latestIgnored := LatestIgnoredVersion()

	expectIgnorePatternSeed(mock)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	expectDoltStatusRows(mock)
	// The seed changed rows (expectIgnorePatternSeed reports RowsAffected=1),
	// so MigrateUp commits it scoped+labeled before the pass runs (#4566: the
	// seed must not ride the per-step pass commits).
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectDoltStatusRows(mock)
	// MigrateUp probes the aux-rekey crash sentinel (bd-578h9.16); this
	// mocked world has no local_metadata table, so no crashed pass.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// MigrateUp captures the pre-pass main cursor for the aux re-key
	// watershed (bd-578h9.4) before the main migrations run.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	if latest == 53 {
		// The v53 pre-repair probes the six rig/agent columns on issues and
		// then the local wisp_dependencies table; this mocked world has all
		// issue columns and no local wisp_dependencies table, so no ALTERs follow.
		for _, col := range []string{"hook_bead", "role_bead", "agent_state", "last_activity", "role_type", "rig"} {
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
				WithArgs("issues", col).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		}
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WithArgs("wisp_dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	}
	// Per-step commit (#4566) snapshots the working set before the migration
	// runs so it can force-stage only the tables this step newly dirties.
	expectDoltStatusRows(mock)
	mock.ExpectExec("(?s).*").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO schema_migrations (version, content_hash) VALUES (?, ?)")).
		WithArgs(latest, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Per-step commit (#4566): re-read the working set (no table newly dirtied
	// in this mocked world), force-stage the cursor table, and commit the step.
	expectDoltStatusRows(mock)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs("schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
	// rekeyDependencyIDs probes whether each edge table has an id column; this
	// mocked world has no such table, so both probes return 0 and the re-key
	// no-ops without scanning/updating rows.
	expectColumnExists(mock, false)
	expectColumnExists(mock, false)
	// rekeyAuxRowIDs reads the ignored cursor to see whether its clone-local
	// marker is pending; at latest it is not, so the re-key no-ops.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", latestIgnored)
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS ignored_schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", latestIgnored)
	expectDoltStatusRows(mock)
	expectDoltStatusRows(mock)
	mock.ExpectQuery("(?s)SELECT t\\.TABLE_NAME\\s+FROM INFORMATION_SCHEMA\\.TABLES t").
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME"}).AddRow("schema_migrations"))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs("schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: apply migrations')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectColumnExists mocks the INFORMATION_SCHEMA.COLUMNS probe still used by
// the dependency/aux id-column re-key paths (dep_id_backfill.go).
func expectColumnExists(mock sqlmock.Sqlmock, present bool) {
	n := 0
	if present {
		n = 1
	}
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
}

// expectContentHashColumnExists mocks the idempotent ensureContentHashColumn
// probe, reporting that the content_hash column already exists (so no ALTER
// runs). The probe is a single-table SHOW COLUMNS, not an
// INFORMATION_SCHEMA scan.
func expectContentHashColumnExists(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SHOW COLUMNS FROM \w+ LIKE 'content_hash'`).
		WillReturnRows(showColumnsRows("content_hash"))
}

func expectScalar(mock sqlmock.Sqlmock, query, column string, value any) {
	mock.ExpectQuery(regexp.QuoteMeta(query)).
		WillReturnRows(sqlmock.NewRows([]string{column}).AddRow(value))
}

func expectDoltStatusRows(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
}
