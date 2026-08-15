package schema

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
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

	// Pre-lock fast-path probe: pass sentinel current and seed present, but the
	// main cursor is behind, so migration work is needed and the locked pass runs.
	expectPassSentinelCurrent(mock)
	expectSeedPatternsPresent(mock, doltIgnorePatterns)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion()-1)

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

// TestMigrateUpWithLockSkipsLockWhenCurrent is the fleet-load guard: a database
// that is already fully migrated and seeded must be probed read-only and never
// touch GET_LOCK — otherwise every command in every concurrent bd process
// serializes on the per-database advisory lock just to discover there is no
// work. sqlmock's ordered expectations fail the test on any GET_LOCK,
// RELEASE_LOCK, INSERT IGNORE seed, or DOLT_COMMIT this path would issue.
func TestMigrateUpWithLockSkipsLockWhenCurrent(t *testing.T) {
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

	expectPassSentinelCurrent(mock)
	expectSeedPatternsPresent(mock, doltIgnorePatterns)
	expectNoMigrationWork(mock)

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (an already-current database must skip GET_LOCK and the migration pass): %v", err)
	}
}

// TestMigrateUpWithLockReseedsUnderLockWhenSeedMissing: an under-seeded
// database must NOT take the fast path — the missing pattern sends it through
// the locked pass, which reseeds and commits the heal exactly as before.
func TestMigrateUpWithLockReseedsUnderLockWhenSeedMissing(t *testing.T) {
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

	// Fast-path probe: sentinel current, but one canonical pattern missing ->
	// not current.
	expectPassSentinelCurrent(mock)
	expectSeedPatternsPresent(mock, doltIgnorePatterns[1:])

	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	// Under the lock, MigrateUp reseeds (rows change), finds no migration
	// work, and commits the seed scoped and labeled. The sentinel is already
	// current, so no restamp follows.
	expectIgnorePatternSeed(mock)
	expectNoMigrationWork(mock)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')")).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	expectPassSentinelCurrent(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (under-seeded database must reseed under the lock): %v", err)
	}
}

// TestMigrateUpWithLockFallsThroughOnProbeError: a fast-path probe failure must
// not fail the open — it falls through to the locked pass, which re-checks and
// surfaces any genuine error itself.
func TestMigrateUpWithLockFallsThroughOnProbeError(t *testing.T) {
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

	expectPassSentinelCurrent(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore")).
		WillReturnError(errors.New("transient catalog race"))

	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectIgnorePatternSeedNoop(mock)
	expectNoMigrationWork(mock)
	expectPassSentinelCurrent(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (probe error must fall through to the locked pass): %v", err)
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
	expectNoMigrationWork(mock)
	// The seed inserted rows and no migration pass follows to commit them, so
	// MigrateUp must commit the heal itself, scoped and labeled.
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')")).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	expectPassSentinelCurrent(mock)

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
	expectNoMigrationWork(mock)
	expectPassSentinelCurrent(mock)

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

// TestMigrateUpSucceedsWhenPassSentinelStampFails pins the review finding that
// a failed sentinel stamp must not fail an already-successful migration. The
// row only enables an optimization: without it every later open takes the
// migration lock, which is the pre-sentinel behavior and always correct.
// Returning the stamp error would report a migration failure to a caller whose
// database is in fact fully current, and invite a retry of a pass with nothing
// left to do.
func TestMigrateUpSucceedsWhenPassSentinelStampFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	expectIgnorePatternSeedNoop(mock)
	expectNoMigrationWork(mock)
	// Sentinel absent, so the pass tries to stamp it — and the write fails
	// (e.g. a read-only or otherwise wedged local_metadata).
	expectPassSentinel(mock, sqlmock.NewRows([]string{"value"}))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS local_metadata`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)")).
		WithArgs(migrationPassCompleteKey, fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion())).
		WillReturnError(errors.New("local_metadata is read-only"))

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v; a failed pass-completion stamp must warn and degrade to locked opens, not fail a migration that already succeeded", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUp() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockLocksWhenPassSentinelMissing pins the review finding on
// the fast path's interleaving hazard: probe inputs alone (seeds present, both
// cursors at latest, hash columns, backfill done) are all satisfied MID-PASS by
// another process's per-step commits — before its dependency rekey and final
// staging commit have run. The database state a prober observes at that moment
// is exactly "everything current, pass-completion sentinel absent" (the running
// pass cleared it at pass start). A prober seeing that state must NOT take the
// lock-free fast path: it must queue on GET_LOCK like pre-fast-path code, and
// the locked pass restamps the sentinel once it proves the database current.
func TestMigrateUpWithLockLocksWhenPassSentinelMissing(t *testing.T) {
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

	// Fast-path probe: sentinel row absent -> not current, no further probe
	// reads, straight to the locked pass.
	expectPassSentinel(mock, sqlmock.NewRows([]string{"value"}))

	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	// Under the lock: reseed no-ops, no migration work — and the sentinel is
	// (re)stamped so subsequent opens regain the fast path.
	expectIgnorePatternSeedNoop(mock)
	expectNoMigrationWork(mock)
	expectPassSentinel(mock, sqlmock.NewRows([]string{"value"}))
	expectPassSentinelStamp(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (missing pass-completion sentinel must send the open through the lock and restamp): %v", err)
	}
}

// TestMigrateUpWithLockLocksWhenPassSentinelStale: a sentinel stamped by an
// older binary (lower main version) proves a complete pass finished — but not
// through THIS binary's migrations. The probe must refuse the fast path.
func TestMigrateUpWithLockLocksWhenPassSentinelStale(t *testing.T) {
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

	stale := fmt.Sprintf("%d/%d", LatestVersion()-1, LatestIgnoredVersion())
	expectPassSentinel(mock, sqlmock.NewRows([]string{"value"}).AddRow(stale))

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
		t.Fatalf("unmet SQL expectations (stale pass-completion sentinel must send the open through the lock): %v", err)
	}
}

// expectPassSentinel mocks the read of the pass-completion sentinel row.
func expectPassSentinel(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnRows(rows)
}

// expectPassSentinelCurrent mocks the sentinel read returning this binary's
// own latest versions — a complete pass has finished at (or past) them.
func expectPassSentinelCurrent(mock sqlmock.Sqlmock) {
	value := fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion())
	expectPassSentinel(mock, sqlmock.NewRows([]string{"value"}).AddRow(value))
}

// expectPassSentinelStamp mocks the ensure-table + REPLACE pair that records
// pass completion.
func expectPassSentinelStamp(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS local_metadata`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)")).
		WithArgs(migrationPassCompleteKey, fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion())).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectNoMigrationWork mocks migrationWorkNeeded reporting a fully current
// database: both cursors at latest, both content_hash columns present, custom
// backfill done.
func expectNoMigrationWork(mock sqlmock.Sqlmock) {
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
}

// expectSeedPatternsPresent mocks the fast-path read-only seed probe
// (doltIgnoreSeedCurrent) returning the given patterns as present rows.
func expectSeedPatternsPresent(mock sqlmock.Sqlmock, patterns []string) {
	rows := sqlmock.NewRows([]string{"pattern"})
	for _, pattern := range patterns {
		rows.AddRow(pattern)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore")).
		WillReturnRows(rows)
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
	// Work is needed: the pass revokes the fast path before its first
	// mutation. This mocked world has no local_metadata table (the
	// INFORMATION_SCHEMA probe below reports it absent), so the DELETE hits
	// table-not-exist — tolerated, the sentinel simply has nothing to clear.
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnError(&mysql.MySQLError{Number: 1146, Message: "table not found: local_metadata"})
	expectDoltStatusRows(mock)
	// The seed changed rows (expectIgnorePatternSeed reports RowsAffected=1),
	// so MigrateUp commits it scoped+labeled before the pass runs (#4566: the
	// seed must not ride the per-step pass commits).
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')")).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
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
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs("schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
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
	// DOLT_ADD and DOLT_COMMIT run through drainCall (QueryContext) so their
	// proc result sets are consumed on the pinned conn; mock them as queries.
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs("schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: apply migrations')")).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	// The pass finished: only now is the completion sentinel stamped, making
	// the fast path satisfiable again. Ordered expectations pin the invariant
	// the review demanded: no REPLACE before the final DOLT_COMMIT above.
	expectPassSentinel(mock, sqlmock.NewRows([]string{"value"}))
	expectPassSentinelStamp(mock)
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

// expectDirtyGuardRefusal mocks a MigrateUp invocation that walks up to the
// #4566 pre-flight guard and gets refused: the cursor is one migration behind,
// `issues` is dirty in the working set, and the pending (latest) migration
// touches `issues`. This is the interrupted-fresh-bootstrap shape from
// gastownhall/beads#5012 — a previous attempt's step debris, read by a retry.
// It relies on the latest migration touching `issues`; if a future latest
// migration stops doing so, sqlmock will fail loudly on the unexpected query
// flow and this helper should dirty a table that migration does touch.
func expectDirtyGuardRefusal(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()

	latest := LatestVersion()

	expectIgnorePatternSeedNoop(mock)
	// migrationWorkNeeded: main cursor behind -> work needed (short-circuits).
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	// Work is needed: the pass revokes the fast path before its first
	// mutation. This mocked world has no local_metadata table (the
	// INFORMATION_SCHEMA probe below reports it absent), so the DELETE hits
	// table-not-exist — tolerated, the sentinel simply has nothing to clear.
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnError(&mysql.MySQLError{Number: 1146, Message: "table not found: local_metadata"})
	// dirtyBeforeAll: `issues` dirty (working set only, not staged).
	expectDoltStatusDirtyIssues(mock)
	// Nothing staged -> no unstage exec; seed was a no-op -> no seed commit.
	// committableDirtyTables re-reads dolt_status (ignored tables excluded).
	expectDoltStatusDirtyIssues(mock)
	// auxRekeyResumePending: no local_metadata table, no crashed rekey pass.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// pendingMigrationDirtyTables: cursor read, then the pending latest
	// migration's SQL touches `issues` -> DirtyTablesError.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
}

func expectDoltStatusDirtyIssues(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("issues", false))
}

// TestMigrateUpWithLockDirtyGuardStaysFatalWithoutHeal pins the default
// behavior: without WithFreshBootstrapHeal, the #4566 guard refusal surfaces
// as *DirtyTablesError and no DOLT_RESET runs (sqlmock's ordered expectations
// fail the test on any unexpected reset call).
func TestMigrateUpWithLockDirtyGuardStaysFatalWithoutHeal(t *testing.T) {
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
	expectDirtyGuardRefusal(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("MigrateUpWithLock() error = %v, want *DirtyTablesError", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockFreshBootstrapHealResetsAndRetries is the unit-level
// regression for gastownhall/beads#5012: with WithFreshBootstrapHeal (the
// caller created the database within this init), a #4566 guard refusal is
// healed under the held migration lock — DOLT_RESET('--hard') discards the
// interrupted bootstrap's working-set debris and the pass re-runs to
// completion on the same session.
func TestMigrateUpWithLockFreshBootstrapHealResetsAndRetries(t *testing.T) {
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
	// First MigrateUp: refused by the dirty guard.
	expectDirtyGuardRefusal(t, mock)
	// Heal: discard the bootstrap debris on the same locked session.
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	// Second MigrateUp: clean working set, one pending migration applies.
	expectOnePendingMigration(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb", WithFreshBootstrapHeal())
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
