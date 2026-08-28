package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
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
	expectConvergedFastPathMiss(mock, "testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectOnePendingMigration(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb", WithDatabaseSelector(testDatabaseSelector))
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

func TestMigrateUpWithLockPreparationErrorReleasesAndJoinsReleaseFailure(t *testing.T) {
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
	expectConvergedFastPathMiss(mock, "testdb")
	preparationErr := errors.New("bootstrap preparation failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnError(errors.New("release failed"))

	called := 0
	applied, err := MigrateUpWithLock(ctx, conn, "testdb", WithDatabaseSelector(testDatabaseSelector), WithLockedPreparation("tcp:test", func(context.Context, *sql.Conn) (*FreshBootstrapHealCapability, error) {
		called++
		return nil, preparationErr
	}))
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if called != 1 {
		t.Fatalf("locked preparation calls = %d, want 1", called)
	}
	if !errors.Is(err, preparationErr) {
		t.Fatalf("MigrateUpWithLock() error = %v, want preparation error", err)
	}
	if !errors.Is(err, ErrMigrationLockRelease) || !IsMigrationLockError(err) {
		t.Fatalf("MigrateUpWithLock() error = %v, want classifiable release failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockMigrationErrorNotMaskedByReleaseFailure covers the
// sibling failure shape of the preparation-error case above: MigrateUp itself
// fails AND the deferred lock release also fails. The combiner must not
// introduce a separator newline between the two errors (for single-line
// constituent errors like these fixtures the whole message stays on one
// line), and the primary migration failure must lead, so a last-line-only
// capture (`tail -1` in a triage script) cannot see only the generic release
// wrapper. Classification is unchanged from the case above.
func TestMigrateUpWithLockMigrationErrorNotMaskedByReleaseFailure(t *testing.T) {
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
	expectConvergedFastPathMiss(mock, "testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	// MigrateUp's first statement fails with a structural error, standing in
	// for a migration hard-failing on a malformed clone.
	migrationCause := errors.New("column 'is_blocked' could not be found in any table in scope")
	releaseCause := errors.New("driver: bad connection")
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WillReturnError(migrationCause)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnError(releaseCause)

	applied, err := MigrateUpWithLock(ctx, conn, "testdb", WithDatabaseSelector(testDatabaseSelector))
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if err == nil {
		t.Fatal("MigrateUpWithLock() error = nil, want the migration failure")
	}

	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Fatalf("MigrateUpWithLock() error is multi-line, want one line so a last-line-only capture cannot drop the primary error: %q", msg)
	}
	primaryIdx := strings.Index(msg, "is_blocked")
	releaseIdx := strings.Index(msg, "release migration lock")
	if primaryIdx < 0 {
		t.Fatalf("MigrateUpWithLock() error = %q, want the primary migration diagnostic present", msg)
	}
	if releaseIdx < 0 {
		t.Fatalf("MigrateUpWithLock() error = %q, want the lock-release diagnostic present too", msg)
	}
	if primaryIdx > releaseIdx {
		t.Fatalf("MigrateUpWithLock() error = %q, want the primary migration error printed before the lock-release error", msg)
	}

	if !errors.Is(err, migrationCause) {
		t.Fatalf("MigrateUpWithLock() error = %v, want errors.Is against the original migration cause", err)
	}
	if !errors.Is(err, releaseCause) {
		t.Fatalf("MigrateUpWithLock() error = %v, want errors.Is against the original release cause", err)
	}
	if !errors.Is(err, ErrMigrationLockRelease) || !IsMigrationLockError(err) {
		t.Fatalf("MigrateUpWithLock() error = %v, want classifiable release failure", err)
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

	expectIgnorePatternSeed(mock, LatestVersion())
	// migrationWorkNeeded: both cursors at latest, both content_hash columns
	// present, no custom backfill pending -> no work, MigrateUp short-circuits.
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectIgnoredSentinelProbes(mock, true)
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
	// The seed inserted rows and no migration pass follows to commit them, so
	// MigrateUp must commit the heal itself, scoped and labeled.
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')")).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))

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

	expectIgnorePatternSeedNoop(mock, LatestVersion())
	// migrationWorkNeeded: no work, MigrateUp short-circuits.
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectIgnoredSentinelProbes(mock, true)
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

// expectedIgnoreSeedCandidates is the pattern set the seed asserts at a given
// main cursor: the canonical set plus the version-gated ones that qualify.
func expectedIgnoreSeedCandidates(mainVersion int) []string {
	candidates := append([]string(nil), doltIgnorePatterns...)
	for _, gated := range versionGatedDoltIgnorePatterns {
		if mainVersion < gated.minMainVersion {
			continue
		}
		candidates = append(candidates, gated.pattern)
	}
	return candidates
}

// expectIgnoreSeedProbe mocks the seed's read half: the cursor probe that
// decides the version-gated patterns, then the single presence SELECT.
// alreadyPresent is what dolt_ignore reports back.
func expectIgnoreSeedProbe(mock sqlmock.Sqlmock, mainVersion int, alreadyPresent []string) {
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", mainVersion)
	args := make([]driver.Value, 0, len(doltIgnorePatterns)+len(versionGatedDoltIgnorePatterns))
	for _, pattern := range expectedIgnoreSeedCandidates(mainVersion) {
		args = append(args, pattern)
	}
	rows := sqlmock.NewRows([]string{"pattern"})
	for _, pattern := range alreadyPresent {
		rows.AddRow(pattern)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore WHERE pattern IN (")).
		WithArgs(args...).
		WillReturnRows(rows)
}

// expectIgnorePatternSeed mocks the dolt_ignore pattern seed MigrateUp runs
// before anything else on an UNDER-SEEDED database: the probe finds nothing,
// so every pattern is inserted (RowsAffected=1). mainVersion is what the
// seed's cursor probe reports; version-gated patterns (events, >= 0062) are
// only expected when it qualifies them.
func expectIgnorePatternSeed(mock sqlmock.Sqlmock, mainVersion int) {
	expectIgnoreSeedProbe(mock, mainVersion, nil)
	for _, pattern := range expectedIgnoreSeedCandidates(mainVersion) {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

// expectIgnorePatternSeedNoop mocks the seed on a healthy database: the probe
// finds every pattern already registered, so NO statement is issued at all.
// This is the shape a fenced wire client must produce — it holds no write
// grant on dolt_ignore, so an INSERT here is an error, not a no-op.
func expectIgnorePatternSeedNoop(mock sqlmock.Sqlmock, mainVersion int) {
	expectIgnoreSeedProbe(mock, mainVersion, expectedIgnoreSeedCandidates(mainVersion))
}

func expectOnePendingMigration(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()

	latest := LatestVersion()
	latestIgnored := LatestIgnoredVersion()

	expectIgnorePatternSeed(mock, latest-1)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
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
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectContentHashColumnExists(mock)
	expectCursorProbe(mock, "schema_migrations", true)
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
	// The pending (latest) migration is 0063_create_provenance_events, a
	// plain CREATE TABLE body with no bare CALL — execMigrationBody routes
	// such bodies through ExecContext, not DrainCall.
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
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", latestIgnored)
	expectIgnoredSentinelProbes(mock, true)
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS ignored_schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectContentHashColumnExists(mock)
	// The applier reads the ignored cursor through the guarded currentVersion
	// (gh 5033), so a non-zero cursor is followed by the sentinel probes here
	// too, not just in migrationWorkNeeded.
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", latestIgnored)
	expectIgnoredSentinelProbes(mock, true)
	expectDoltStatusRows(mock)
	expectDoltStatusRows(mock)
	mock.ExpectQuery("(?s)SELECT t\\.TABLE_NAME\\s+FROM INFORMATION_SCHEMA\\.TABLES t").
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME"}).AddRow("schema_migrations"))
	// DOLT_ADD and DOLT_COMMIT run through DrainCall (QueryContext) so their
	// proc result sets are consumed on the pinned conn; mock them as queries.
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs("schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: apply migrations')")).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
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

// expectCursorProbe mocks the information_schema existence probe that
// migrationSource.currentVersion issues before it ever reads the cursor
// table. The probe was added by be-bv7x so a not-yet-created cursor table
// never poisons the pooled Dolt session with a failing statement.
func expectCursorProbe(mock sqlmock.Sqlmock, table string, exists bool) {
	present := 0
	if exists {
		present = 1
	}
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.tables`).
		WithArgs(table).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(present))
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
// eventsFlipVersion is 0062_events_dolt_ignore, the migration whose body
// drops and recreates `events`. The dirty-guard shape below needs a pending
// migration that touches the dirty table, so the mocked cursor is pinned just
// below THIS version rather than to an offset from LatestVersion() — an offset
// silently stops covering 0062 the moment anything newer lands (it did, when
// 0063/0064 arrived).
const eventsFlipVersion = 62

func expectDirtyGuardRefusal(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()

	cursor := eventsFlipVersion - 1

	expectIgnorePatternSeedNoop(mock, cursor)
	// migrationWorkNeeded: main cursor behind -> work needed (short-circuits).
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", cursor)
	// dirtyBeforeAll: `events` dirty (working set only, not staged).
	expectDoltStatusDirtyEvents(mock)
	// Nothing staged -> no unstage exec; seed was a no-op -> no seed commit.
	// committableDirtyTables re-reads dolt_status (ignored tables excluded).
	expectDoltStatusDirtyEvents(mock)
	// auxRekeyResumePending: no local_metadata table, no crashed rekey pass.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// pendingMigrationDirtyTables: cursor read, then pending 0062's SQL
	// touches `events` -> DirtyTablesError.
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", cursor)
}

func expectDoltStatusDirtyEvents(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("events", false))
}

const (
	testBootstrapEndpoint    = "tcp:127.0.0.1:3307"
	testBootstrapServerUUID  = "11111111-2222-3333-4444-555555555555"
	testBootstrapInitialHead = "abcdefghijklmnopqrstuvwx12345678"
)

func testFreshBootstrapHealCapability() *FreshBootstrapHealCapability {
	return &FreshBootstrapHealCapability{
		endpoint:     testBootstrapEndpoint,
		serverUUID:   testBootstrapServerUUID,
		databaseName: "testdb",
		initialHead:  testBootstrapInitialHead,
	}
}

func expectFreshBootstrapIdentityMatch(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid")).
		WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid"}).
			AddRow("testdb", testBootstrapServerUUID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?")).
		WithArgs(testBootstrapInitialHead).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
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
	expectConvergedFastPathMiss(mock, "testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectDirtyGuardRefusal(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb", WithDatabaseSelector(testDatabaseSelector))
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
	// Heal: revalidate the exact database incarnation, atomically consume the
	// capability, and discard the bootstrap debris on the same locked session.
	expectFreshBootstrapIdentityMatch(mock)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	// Second MigrateUp: clean working set, one pending migration applies.
	expectOnePendingMigration(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb",
		WithFreshBootstrapHeal(testFreshBootstrapHealCapability(), testBootstrapEndpoint))
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

// expectIgnoredSentinelProbes mocks the INFORMATION_SCHEMA lookups
// currentVersion issues to confirm a non-zero ignored cursor against the
// schema it claims (gh 5033). They fire only for a non-zero cursor, in
// ignoredSource's table then column sentinel order.
func expectIgnoredSentinelProbes(mock sqlmock.Sqlmock, present bool) {
	count := 0
	if present {
		count = 1
	}
	for range ignoredSource.sentinelTables {
		mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.TABLES")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
	}
	if present {
		for range ignoredSource.sentinelColumns {
			mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.COLUMNS")).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		}
	}
}

func TestMigrateUpWithLockFreshBootstrapHealProbeFailuresStayFatal(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		expectProbe func(sqlmock.Sqlmock)
	}{
		{
			name:     "endpoint mismatch",
			endpoint: "tcp:127.0.0.1:9999",
		},
		{
			name:     "identity query failure",
			endpoint: testBootstrapEndpoint,
			expectProbe: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid")).
					WillReturnError(errors.New("identity probe failed"))
			},
		},
		{
			name:     "database mismatch",
			endpoint: testBootstrapEndpoint,
			expectProbe: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid")).
					WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid"}).
						AddRow("replacement", testBootstrapServerUUID))
			},
		},
		{
			name:     "server mismatch",
			endpoint: testBootstrapEndpoint,
			expectProbe: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid")).
					WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid"}).
						AddRow("testdb", "replacement-server"))
			},
		},
		{
			name:     "ancestry query failure",
			endpoint: testBootstrapEndpoint,
			expectProbe: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid")).
					WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid"}).
						AddRow("testdb", testBootstrapServerUUID))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?")).
					WithArgs(testBootstrapInitialHead).
					WillReturnError(errors.New("ancestry probe failed"))
			},
		},
		{
			name:     "initial head missing from ancestry",
			endpoint: testBootstrapEndpoint,
			expectProbe: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid")).
					WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid"}).
						AddRow("testdb", testBootstrapServerUUID))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?")).
					WithArgs(testBootstrapInitialHead).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			if tt.expectProbe != nil {
				tt.expectProbe(mock)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
				WithArgs(lockName).
				WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

			capability := testFreshBootstrapHealCapability()
			applied, err := MigrateUpWithLock(ctx, conn, "testdb",
				WithFreshBootstrapHeal(capability, tt.endpoint))
			if applied != 0 {
				t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
			}
			var dirtyErr *DirtyTablesError
			if !errors.As(err, &dirtyErr) {
				t.Fatalf("MigrateUpWithLock() error = %v, want original *DirtyTablesError", err)
			}
			if err != dirtyErr {
				t.Fatalf("MigrateUpWithLock() wrapped or replaced DirtyTablesError: %T %v", err, err)
			}
			if capability.consumed.Load() {
				t.Fatal("failed identity probe consumed capability")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

func TestMigrateUpWithLockFreshBootstrapHealCapabilityIsOneShot(t *testing.T) {
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
	capability := testFreshBootstrapHealCapability()
	option := WithFreshBootstrapHeal(capability, testBootstrapEndpoint)

	// First locked pass consumes the capability before reset, then its single
	// rerun fails transiently. This is the shape that causes an outer retry.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectDirtyGuardRefusal(t, mock)
	expectFreshBootstrapIdentityMatch(mock)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	// The reset returns to the v60 HEAD used by expectDirtyGuardRefusal. The
	// rerun repeats main's unconditional ignore-pattern seed before reaching
	// migrationWorkNeeded, where the injected transient failure occurs.
	expectIgnorePatternSeedNoop(mock, LatestVersion()-2)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion()-2)
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnError(errors.New("connection reset"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	if _, err := MigrateUpWithLock(ctx, conn, "testdb", option); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("first MigrateUpWithLock() error = %v, want transient rerun failure", err)
	}
	if !capability.consumed.Load() {
		t.Fatal("successful reset attempt did not consume capability")
	}

	// A later outer retry may encounter another dirty guard, but the consumed
	// capability returns that original refusal without a second reset.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectDirtyGuardRefusal(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb", option)
	if applied != 0 {
		t.Fatalf("second MigrateUpWithLock() applied = %d, want 0", applied)
	}
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("second MigrateUpWithLock() error = %v, want original *DirtyTablesError", err)
	}
	if err != dirtyErr {
		t.Fatalf("second MigrateUpWithLock() wrapped or replaced DirtyTablesError: %T %v", err, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
