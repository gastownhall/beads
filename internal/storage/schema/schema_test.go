package schema

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestPendingMigrationDirtyTablesDetectsMigration0043Dependencies(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(42))

	touched, err := mainSource.pendingMigrationDirtyTables(context.Background(), db, map[string]dirtyTableState{
		"dependencies": {},
	})
	if err != nil {
		t.Fatalf("pendingMigrationDirtyTables: %v", err)
	}
	if len(touched) != 1 || touched[0] != "dependencies" {
		t.Fatalf("touched = %v, want [dependencies]", touched)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestMigrateUpReturnsDirtyTablesErrorForPreExistingDirtyTable exercises the
// full MigrateUp pre-flight guard (gastownhall/beads#4566): a pre-existing
// dirty `dependencies` table collides with pending migration 0043, which
// alters it (confirmed against the real migration content by
// TestPendingMigrationDirtyTablesDetectsMigration0043Dependencies above).
// MigrateUp must report this with the typed *DirtyTablesError so
// working-set-reconcile opens can detect it via errors.As and skip the
// migration instead of failing outright.
func TestMigrateUpReturnsDirtyTablesErrorForPreExistingDirtyTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// MigrateUp re-asserts the canonical dolt_ignore patterns before anything
	// else (GH#4378); the rows changed, so a scoped commit lands before the
	// pass runs (#4566: the seed must not ride the per-step pass commits).
	expectIgnorePatternSeed(mock)
	// migrationWorkNeeded: mainSource.atLatest reads the current cursor; v42
	// is behind LatestVersion(), so the || short-circuits before checking
	// ignoredSource.atLatest or the content-hash/backfill probes.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", 42)

	// dirtyTables(ctx, db, false): `dependencies` has an uncommitted, unstaged
	// change in the working set.
	expectDirtyDoltStatusRow(mock, "dependencies", false)
	// The seed changed rows, so it is committed scoped+labeled right after
	// pre-existing tables are unstaged, before the dirty-table guards run.
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// committableDirtyTables -> dirtyTables(ctx, db, true): same dirty state.
	expectDirtyDoltStatusRow(mock, "dependencies", false)

	// auxRekeyResumePending: no local_metadata table, so no resume in flight.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// pendingMigrationDirtyTables re-reads the current version and finds
	// migration 0043 touches the dirty `dependencies` table.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", 42)

	_, err = MigrateUp(context.Background(), db)
	if err == nil {
		t.Fatal("MigrateUp() error = nil, want *DirtyTablesError")
	}
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("MigrateUp() error = %v (%T), want *DirtyTablesError", err, err)
	}
	if len(dirtyErr.Tables) != 1 || dirtyErr.Tables[0] != "dependencies" {
		t.Fatalf("DirtyTablesError.Tables = %v, want [dependencies]", dirtyErr.Tables)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// expectDirtyDoltStatusRow mocks a dolt_status query returning a single dirty
// table row. The regex matches both the plain and dolt_ignore-filtered forms
// of the dirtyTables query (see lock_test.go's expectDoltStatusRows).
func expectDirtyDoltStatusRow(mock sqlmock.Sqlmock, table string, staged bool) {
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow(table, staged))
}

func TestIgnoredPendingMigrationDirtyTablesDetectsWispDependencies(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM ignored_schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(2))

	touched, err := ignoredSource.pendingMigrationDirtyTables(context.Background(), db, map[string]dirtyTableState{
		"wisp_dependencies": {},
	})
	if err != nil {
		t.Fatalf("pendingMigrationDirtyTables: %v", err)
	}
	if len(touched) != 1 || touched[0] != "wisp_dependencies" {
		t.Fatalf("touched = %v, want [wisp_dependencies]", touched)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestMigrationSQLTouchesTableStatementForms(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{
			name: "rename table source",
			sql:  "RENAME TABLE dependencies TO old_dependencies",
			want: true,
		},
		{
			name: "rename table target",
			sql:  "RENAME TABLE old_dependencies TO dependencies",
			want: true,
		},
		{
			name: "create index on table",
			sql:  "CREATE INDEX idx_dep_type ON dependencies (type)",
			want: true,
		},
		{
			name: "create unique index on quoted table",
			sql:  "CREATE UNIQUE INDEX idx_dep_type ON `dependencies` (type)",
			want: true,
		},
		{
			name: "create view named table",
			sql:  "CREATE OR REPLACE VIEW dependencies AS SELECT 1",
			want: true,
		},
		{
			name: "select only",
			sql:  "SELECT * FROM dependencies",
			want: false,
		},
		{
			name: "unrelated ddl",
			sql:  "ALTER TABLE comments ADD COLUMN reviewed_at DATETIME",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := migrationSQLTouchesTable(tt.sql, "dependencies"); got != tt.want {
				t.Fatalf("migrationSQLTouchesTable(%q) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}

func TestCheckNoDuplicateVersionsPanicsWithBothFilenames(t *testing.T) {
	files := []migrationFile{
		{version: 7, name: "0007_create_metadata.up.sql"},
		{version: 12, name: "0012_create_routes.up.sql"},
		{version: 7, name: "0007_create_duplicate.up.sql"},
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate version, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		for _, want := range []string{
			"duplicate migration version 7",
			"0007_create_metadata.up.sql",
			"0007_create_duplicate.up.sql",
			"renumber one before commit",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic message %q missing expected substring %q", msg, want)
			}
		}
	}()
	checkNoDuplicateVersions(files)
}

func TestDirtyTableSignatureRejectsUnsafeTableName(t *testing.T) {
	_, err := dirtyTableSignature(context.Background(), nil, "issues'); SELECT 1; --")
	if err == nil {
		t.Fatal("expected unsafe table name error")
	}
	if !strings.Contains(err.Error(), "unsafe dolt status table name") {
		t.Fatalf("error = %v, want unsafe table name context", err)
	}
}

func TestMigration0035HandlesLegacyWispDependenciesShape(t *testing.T) {
	upSQL, err := os.ReadFile("migrations/0035_migrate_infra_to_wisps.up.sql")
	if err != nil {
		t.Fatalf("read 0035 up migration: %v", err)
	}
	downSQL, err := os.ReadFile("migrations/0035_migrate_infra_to_wisps.down.sql")
	if err != nil {
		t.Fatalf("read 0035 down migration: %v", err)
	}

	up := string(upSQL)
	for _, want := range []string{
		"@has_split_wisp_dependencies",
		"INSERT IGNORE INTO wisp_dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)",
		"INSERT IGNORE INTO wisp_dependencies (issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id)",
	} {
		if !strings.Contains(up, want) {
			t.Fatalf("0035 up migration missing legacy/split branch marker %q", want)
		}
	}

	down := string(downSQL)
	for _, want := range []string{
		"@has_split_wisp_dependencies",
		"SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id FROM wisp_dependencies",
		"SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external), type, created_at, created_by, metadata, thread_id FROM wisp_dependencies",
	} {
		if !strings.Contains(down, want) {
			t.Fatalf("0035 down migration missing legacy/split branch marker %q", want)
		}
	}
}

func TestMigration0053RepairsRigWispsShape(t *testing.T) {
	sql, err := os.ReadFile("migrations/0053_repair_rig_wisps.up.sql")
	if err != nil {
		t.Fatalf("read 0053 up migration: %v", err)
	}

	body := string(sql)
	for _, want := range []string{
		"@has_wisps",
		"INFORMATION_SCHEMA.TABLES",
		"INSERT IGNORE INTO issues",
		"FROM wisps WHERE issue_type = ''rig''",
		"SET ephemeral = 0",
		"INSERT IGNORE INTO dependencies",
		"FROM wisp_dependencies wd",
		"REPLACE INTO dependencies",
		"REPLACE INTO wisp_dependencies",
		"wisp_child_counters",
		"INSERT IGNORE INTO child_counters",
		"UPDATE child_counters",
		"DELETE FROM wisps WHERE issue_type = ''rig''",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("0053 migration missing rig repair marker %q", want)
		}
	}
}

func TestEnsureIssuesRigColumnsAddsOnlyMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// #4502: issues tables bootstrapped before the rig/agent columns landed
	// in 0001 reach v52 without them; the 0053 pre-repair must add exactly
	// the missing ones. Simulate hook_bead present, the other five absent.
	countQuery := `SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`
	mock.ExpectQuery(countQuery).WithArgs("issues", "hook_bead").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	for _, col := range []struct{ name, ddl string }{
		{"role_bead", "ALTER TABLE issues ADD COLUMN role_bead VARCHAR\\(255\\) DEFAULT ''"},
		{"agent_state", "ALTER TABLE issues ADD COLUMN agent_state VARCHAR\\(32\\) DEFAULT ''"},
		{"last_activity", "ALTER TABLE issues ADD COLUMN last_activity DATETIME"},
		{"role_type", "ALTER TABLE issues ADD COLUMN role_type VARCHAR\\(32\\) DEFAULT ''"},
		{"rig", "ALTER TABLE issues ADD COLUMN rig VARCHAR\\(255\\) DEFAULT ''"},
	} {
		mock.ExpectQuery(countQuery).WithArgs("issues", col.name).
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
		mock.ExpectExec(col.ddl).WillReturnResult(sqlmock.NewResult(0, 0))
	}

	if err := ensureIssuesRigColumns(context.Background(), db); err != nil {
		t.Fatalf("ensureIssuesRigColumns: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureWispDependenciesSplitTargetsAddsMissingAndBackfills(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	tableQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	mock.ExpectQuery(tableQuery).WithArgs("wisp_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	ddlByColumn := map[string]string{
		"depends_on_issue_id": "ALTER TABLE wisp_dependencies ADD COLUMN depends_on_issue_id VARCHAR\\(255\\) NULL",
		"depends_on_wisp_id":  "ALTER TABLE wisp_dependencies ADD COLUMN depends_on_wisp_id VARCHAR\\(255\\) NULL",
		"depends_on_external": "ALTER TABLE wisp_dependencies ADD COLUMN depends_on_external VARCHAR\\(255\\) NULL",
	}
	for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
		mock.ExpectQuery(columnQuery).WithArgs("wisp_dependencies", col).
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
		mock.ExpectExec(ddlByColumn[col]).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectQuery(columnQuery).WithArgs("wisp_dependencies", "depends_on_id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	for _, update := range []string{
		`UPDATE wisp_dependencies SET depends_on_external = depends_on_id`,
		`UPDATE wisp_dependencies wd JOIN wisps w ON w\.id = wd\.depends_on_id`,
		`UPDATE wisp_dependencies wd JOIN issues i ON i\.id = wd\.depends_on_id`,
		`UPDATE wisp_dependencies SET depends_on_external = depends_on_id WHERE depends_on_external IS NULL`,
	} {
		mock.ExpectExec(update).WillReturnResult(sqlmock.NewResult(0, 1))
	}

	if err := ensureWispDependenciesSplitTargets(context.Background(), db); err != nil {
		t.Fatalf("ensureWispDependenciesSplitTargets: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestPreMigrationRepairScopedToMain0053(t *testing.T) {
	// The repair must not fire for other versions or for the ignored source
	// (whose cursor table differs); nil DB proves no queries are attempted.
	if err := mainSource.preMigrationRepair(context.Background(), nil, 52); err != nil {
		t.Fatalf("main v52 repair = %v, want nil no-op", err)
	}
	if err := ignoredSource.preMigrationRepair(context.Background(), nil, 53); err != nil {
		t.Fatalf("ignored v53 repair = %v, want nil no-op", err)
	}
}

func TestPreMigrationRepairScopedToMain0047(t *testing.T) {
	// #4695/#4176: the repair must not fire for other versions or for the
	// ignored source; nil DB proves no queries are attempted.
	if err := mainSource.preMigrationRepair(context.Background(), nil, 46); err != nil {
		t.Fatalf("main v46 repair = %v, want nil no-op", err)
	}
	if err := ignoredSource.preMigrationRepair(context.Background(), nil, 47); err != nil {
		t.Fatalf("ignored v47 repair = %v, want nil no-op", err)
	}
}

func TestPreMigrationRepairDispatchesMain47ToWispTableRepair(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	tableQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	mock.ExpectQuery(tableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS wisps`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(tableQuery).WithArgs("wisp_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS wisp_dependencies`).WillReturnResult(sqlmock.NewResult(0, 0))

	if err := mainSource.preMigrationRepair(context.Background(), db, 47); err != nil {
		t.Fatalf("main v47 repair: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRunMigrationsSnapshotsDirtyTablesBeforeRepairSoRepairMutationsCommitAtomically
// is the atomicity fix for the finding-#6 gap: preMigrationRepair (which can
// itself mutate a synced table, e.g. ensureDependenciesIDColumn's ALTERs on
// `dependencies`) must run AFTER the per-step "before" dirty-table snapshot,
// not before it -- otherwise the repair's own mutation is misclassified as
// pre-existing dirt to exclude from this step's commit, and a process killed
// between this step and the pass's later commits would leave history
// claiming the migration applied while the repaired table's change was never
// durably recorded (and the version-gated repair hook can never re-run to
// fix it, since its version is no longer pending).
//
// sqlmock enforces in-order call matching by default, so the exact sequence
// registered below IS the assertion: if runMigrations snapshotted dirty
// tables AFTER calling preMigrationRepair (the pre-fix order), the "before"
// dirtyTables query below would run too late (after the repair's ADD COLUMN
// /MODIFY/ADD PRIMARY KEY execs it's supposed to precede) and this test would
// fail with an out-of-order-call error, not a silent pass.
//
// This is a Go-level proof of the call ordering, not a live Dolt dirty/staged
// commit -- that end-to-end proof belongs in
// internal/storage/embeddeddolt (cgo-gated, see TestEmbeddedMigrateRepairedDependenciesIDColumnCommitsAtomicallyWithVersion53_4690).
func TestRunMigrationsSnapshotsDirtyTablesBeforeRepairSoRepairMutationsCommitAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	dirtyStatusQuery := `(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s\s+WHERE NOT EXISTS`
	tableQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`

	// 1. The "before" snapshot: this step's own repair has not run yet, so
	// nothing is dirty.
	mock.ExpectQuery(dirtyStatusQuery).
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))

	// 2. preMigrationRepair(53): ensureIssuesRigColumns (all present, no
	// exec), ensureWispDependenciesSplitTargets (table absent, no-op), then
	// ensureDependenciesIDColumn actually mutating `dependencies`.
	for _, col := range []string{"hook_bead", "role_bead", "agent_state", "last_activity", "role_type", "rig"} {
		mock.ExpectQuery(columnQuery).WithArgs("issues", col).
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	}
	mock.ExpectQuery(tableQuery).WithArgs("wisp_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectQuery(columnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies ADD COLUMN id CHAR\(36\) NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`(?s)SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external\s+FROM dependencies\s+WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dependencies WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	keyColumnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.KEY_COLUMN_USAGE.*TABLE_NAME = \? AND COLUMN_NAME = \? AND CONSTRAINT_NAME = 'PRIMARY'`
	mock.ExpectQuery(keyColumnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies MODIFY COLUMN id CHAR\(36\) NOT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	tableConstraintQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLE_CONSTRAINTS.*TABLE_NAME = \? AND CONSTRAINT_TYPE = 'PRIMARY KEY'`
	mock.ExpectQuery(tableConstraintQuery).WithArgs("dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies ADD PRIMARY KEY \(id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// 3. Migration 0053's own frozen SQL, then its cursor row.
	mock.ExpectExec(`SET FOREIGN_KEY_CHECKS = 0`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(53, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// 4. commitMigrationStep's "after" snapshot sees `dependencies` dirty --
	// from the repair's own ALTERs above, now correctly attributed to THIS
	// step because the "before" snapshot in (1) predates them -- so it gets
	// force-added alongside the cursor table and committed in the same call.
	mock.ExpectQuery(dirtyStatusQuery).
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("dependencies", false))
	mock.ExpectExec(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("dependencies").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CALL DOLT_COMMIT`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	applied, err := runMigrations(context.Background(), db, mainSource, 52, 53, true)
	if err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("runMigrations applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (ordering mismatch would surface here): %v", err)
	}
}

func TestEnsureWispTablesForMigration0047CreatesMissingTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// #4695/#4176: a clone whose main cursor arrives at 0047 with the wisp
	// tables entirely absent (dolt_ignore'd, never synced) must have them
	// created at the canonical shape before 0047's frozen recompute runs.
	tableQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	mock.ExpectQuery(tableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS wisps \(`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(tableQuery).WithArgs("wisp_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS wisp_dependencies \(`).WillReturnResult(sqlmock.NewResult(0, 0))

	if err := ensureWispTablesForMixedBlockedRecompute(context.Background(), db); err != nil {
		t.Fatalf("ensureWispTablesForMixedBlockedRecompute: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureWispTablesForMigration0047DelegatesSplitTargetRepairWhenWispDependenciesExists(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	tableQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	// wisps already exists: skip creating it.
	mock.ExpectQuery(tableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	// wisp_dependencies already exists too: delegate to the same split-target
	// repair migration 0053 relies on, rather than recreating it.
	mock.ExpectQuery(tableQuery).WithArgs("wisp_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(tableQuery).WithArgs("wisp_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
		mock.ExpectQuery(columnQuery).WithArgs("wisp_dependencies", col).
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	}
	// The legacy source column is already gone: a prior pass finished the
	// backfill (or this database never had it), so there is nothing left to
	// re-run and the repair no-ops here.
	mock.ExpectQuery(columnQuery).WithArgs("wisp_dependencies", "depends_on_id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))

	if err := ensureWispTablesForMixedBlockedRecompute(context.Background(), db); err != nil {
		t.Fatalf("ensureWispTablesForMixedBlockedRecompute: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureDependenciesIDColumnNoopWhenAlreadyFullyBackfilledAndKeyed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Steady state: id exists, nothing left to backfill (WHERE id IS NULL
	// matches no rows), and id is already the primary key. Column presence
	// alone must not short-circuit re-verification (#3/#6 re-entry), but a
	// database that has already fully converged still does no work.
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	mock.ExpectQuery(columnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external\s+FROM dependencies\s+WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dependencies WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	keyColumnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.KEY_COLUMN_USAGE.*TABLE_NAME = \? AND COLUMN_NAME = \? AND CONSTRAINT_NAME = 'PRIMARY'`
	mock.ExpectQuery(keyColumnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))

	if err := ensureDependenciesIDColumn(context.Background(), db); err != nil {
		t.Fatalf("ensureDependenciesIDColumn: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureDependenciesIDColumnBackfillsMissingIDsDeterministically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// #4690: dependencies already in split-target/content-derived-key shape
	// but never had its surrogate id column added at all (a different
	// historical migration path than this repo's 0043). Add it, backfill
	// with the same deterministic id every insert path and the post-migration
	// rekey pass use, and restore id as the PRIMARY KEY (0043's exact
	// canonical shape -- not just a plain NOT NULL column, see #4690's
	// duplication risk without a key).
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	mock.ExpectQuery(columnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies ADD COLUMN id CHAR\(36\) NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	rows := sqlmock.NewRows([]string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}).
		AddRow("iss-1", "iss-2", nil, nil).
		AddRow("iss-3", nil, "wisp-1", nil)
	mock.ExpectQuery(`(?s)SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external\s+FROM dependencies\s+WHERE id IS NULL`).
		WillReturnRows(rows)

	id1 := depid.New("iss-1", "iss-2")
	mock.ExpectExec(`(?s)UPDATE dependencies SET id = \?\s+WHERE issue_id = \?.*depends_on_issue_id <=> \?.*depends_on_wisp_id <=> \?.*depends_on_external <=> \?`).
		WithArgs(id1, "iss-1", "iss-2", nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	id2 := depid.New("iss-3", "wisp-1")
	mock.ExpectExec(`(?s)UPDATE dependencies SET id = \?\s+WHERE issue_id = \?.*depends_on_issue_id <=> \?.*depends_on_wisp_id <=> \?.*depends_on_external <=> \?`).
		WithArgs(id2, "iss-3", nil, "wisp-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dependencies WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	keyColumnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.KEY_COLUMN_USAGE.*TABLE_NAME = \? AND COLUMN_NAME = \? AND CONSTRAINT_NAME = 'PRIMARY'`
	mock.ExpectQuery(keyColumnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies MODIFY COLUMN id CHAR\(36\) NOT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	tableConstraintQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLE_CONSTRAINTS.*TABLE_NAME = \? AND CONSTRAINT_TYPE = 'PRIMARY KEY'`
	mock.ExpectQuery(tableConstraintQuery).WithArgs("dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies ADD PRIMARY KEY \(id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := ensureDependenciesIDColumn(context.Background(), db); err != nil {
		t.Fatalf("ensureDependenciesIDColumn: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureDependenciesIDColumnDropsExistingPrimaryKeyBeforeAddingID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// The #4690 drifted shape can leave dependencies keyed some other way
	// (e.g. a composite content-derived key predating the id column). A
	// table carries only one PRIMARY KEY, so the existing one must be
	// dropped before id can become it; the uk_dep_* natural-identity unique
	// keys (0043) enforce the real uniqueness regardless.
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	mock.ExpectQuery(columnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external\s+FROM dependencies\s+WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dependencies WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	keyColumnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.KEY_COLUMN_USAGE.*TABLE_NAME = \? AND COLUMN_NAME = \? AND CONSTRAINT_NAME = 'PRIMARY'`
	mock.ExpectQuery(keyColumnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies MODIFY COLUMN id CHAR\(36\) NOT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	tableConstraintQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLE_CONSTRAINTS.*TABLE_NAME = \? AND CONSTRAINT_TYPE = 'PRIMARY KEY'`
	mock.ExpectQuery(tableConstraintQuery).WithArgs("dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectExec(`ALTER TABLE dependencies DROP PRIMARY KEY`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE dependencies ADD PRIMARY KEY \(id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := ensureDependenciesIDColumn(context.Background(), db); err != nil {
		t.Fatalf("ensureDependenciesIDColumn: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureDependenciesIDColumnFailsClearlyOnTargetlessRowInsteadOfBrickingNotNullModify(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// ck_dep_one_target (0041) should make a targetless row unreachable in
	// practice, but if one exists anyway the repair must fail with a clear,
	// actionable error instead of leaving id NULL under a column callers
	// assume is NOT NULL, or letting a blind MODIFY ... NOT NULL abort with a
	// generic, confusing "column cannot be null" error.
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	mock.ExpectQuery(columnQuery).WithArgs("dependencies", "id").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(`ALTER TABLE dependencies ADD COLUMN id CHAR\(36\) NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	rows := sqlmock.NewRows([]string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}).
		AddRow("iss-4", nil, nil, nil)
	mock.ExpectQuery(`(?s)SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external\s+FROM dependencies\s+WHERE id IS NULL`).
		WillReturnRows(rows)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dependencies WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))

	err = ensureDependenciesIDColumn(context.Background(), db)
	if err == nil {
		t.Fatal("ensureDependenciesIDColumn = nil error, want a targetless-row error")
	}
	if !strings.Contains(err.Error(), "1 dependencies row") {
		t.Fatalf("ensureDependenciesIDColumn error = %q, want it to name the unbackfillable row count", err)
	}
	// The MODIFY COLUMN ... NOT NULL was never attempted (no mock expectation
	// registered for it above): mock.ExpectationsWereMet catches a stray call.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestMigration0057GuardsEventValueColumnsIndependently(t *testing.T) {
	sql, err := os.ReadFile("migrations/0057_events_value_columns_idempotent_longtext.up.sql")
	if err != nil {
		t.Fatalf("read 0057 up migration: %v", err)
	}

	// Each column has its own guard variable and its own MODIFY, not one
	// combined check driving both: a single shared check would either skip
	// converting the other column (if only one had already drifted to
	// LONGTEXT) or re-issue a MODIFY on a column that's already LONGTEXT,
	// re-triggering the encoding-flip risk this migration exists to guard
	// against.
	body := string(sql)
	for _, want := range []string{
		"@old_value_needs_fix",
		"@new_value_needs_fix",
		"INFORMATION_SCHEMA.COLUMNS",
		"COLUMN_TYPE = 'text'",
		"ALTER TABLE events MODIFY COLUMN old_value LONGTEXT",
		"ALTER TABLE events MODIFY COLUMN new_value LONGTEXT",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("0057 migration missing independent per-column guard marker %q", want)
		}
	}
	// The two columns must not be MODIFY'd in one combined ALTER statement
	// (that would put them back behind a single guard).
	if strings.Contains(body, "MODIFY COLUMN old_value LONGTEXT, MODIFY COLUMN new_value LONGTEXT") {
		t.Fatal("0057 migration MODIFYs old_value and new_value in one combined ALTER, want independent guards/ALTERs per column")
	}
}

func TestIgnoredMigration0011CleansOrphanedChildCountersShape(t *testing.T) {
	sql, err := os.ReadFile("migrations/ignored/0011_cleanup_orphaned_child_counters.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0011 up migration: %v", err)
	}

	// #4534: counter rows orphaned while fk_counter_parent was dropped brick
	// all inserts once the FK returns; the cleanup must preserve live-wisp
	// counters and delete only rows dangling from issues.
	body := string(sql)
	for _, want := range []string{
		"@has_child_counters",
		"INSERT IGNORE INTO wisp_child_counters",
		"GREATEST(wcc.last_child, cc.last_child)",
		"DELETE cc FROM child_counters cc INNER JOIN wisps w ON w.id = cc.parent_id",
		"DELETE cc FROM child_counters cc LEFT JOIN issues i ON i.id = cc.parent_id WHERE i.id IS NULL",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ignored 0011 migration missing cleanup marker %q", want)
		}
	}
}

func TestMigration0053NoopsWithoutWispTablesThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "rig-repair-no-wisps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create no-wisps dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	seedSQL := fmt.Sprintf(`
DROP TABLE IF EXISTS wisp_child_counters;
DROP TABLE IF EXISTS wisp_comments;
DROP TABLE IF EXISTS wisp_events;
DROP TABLE IF EXISTS wisp_dependencies;
DROP TABLE IF EXISTS wisp_labels;
DROP TABLE IF EXISTS wisps;
DELETE FROM schema_migrations WHERE version = %d;
`, LatestVersion())
	migrationSQL, err := mainSource.files.ReadFile("migrations/0053_repair_rig_wisps.up.sql")
	if err != nil {
		t.Fatalf("read 0053 migration: %v", err)
	}
	runDoltSQL(t, dir, seedSQL+"\n"+string(migrationSQL))

	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'`, "0")
}

// TestMigration0056NoopsWithoutWispCommentsThroughDoltCLI pins the fresh-clone
// guard for the comments-keyset index: main migrations run before the ignored
// chain materializes the clone-local wisp_% tables, so migration 0056 must
// no-op its wisp half when wisp_comments is absent. A bare
// CREATE INDEX ... ON wisp_comments would fail at PREPARE ("table not found")
// and brick the first writable open. The durable comments half still runs.
// AllMigrationsSQL is main-source only (it never creates wisp_comments), so
// applying it already runs 0056 against an absent wisp_comments; the isolated
// re-apply then asserts the no-op explicitly.
func TestMigration0056NoopsWithoutWispCommentsThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "comments-keyset-no-wisps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create no-wisps dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	seedSQL := fmt.Sprintf(`
DROP TABLE IF EXISTS wisp_comments;
DELETE FROM schema_migrations WHERE version = %d;
`, LatestVersion())
	migrationSQL, err := mainSource.files.ReadFile("migrations/0056_add_comments_keyset_index.up.sql")
	if err != nil {
		t.Fatalf("read 0056 migration: %v", err)
	}
	runDoltSQL(t, dir, seedSQL+"\n"+string(migrationSQL))

	// Wisp half no-oped: no wisp_comments table was created, no error raised.
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments'`, "0")
	// Comments half ran: the durable composite index is present (one distinct
	// index name; a composite spans three STATISTICS rows).
	requireDoltCount(t, dir,
		`SELECT COUNT(DISTINCT INDEX_NAME) AS c FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'comments' AND INDEX_NAME = 'idx_comments_issue_created_id'`, "1")
}

func TestMigration0053RepairsRigWispsThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "rig-repair")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create rig repair dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	const rigID = "schema-cli-rig"
	const targetID = "schema-cli-target"
	const sourceID = "schema-cli-source"
	seedSQL := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS wisp_child_counters (
    parent_id VARCHAR(255) PRIMARY KEY,
    last_child INT NOT NULL DEFAULT 0
);
DELETE FROM schema_migrations WHERE version = %d;
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES (%s, 'target', '', '', '', '', 'open', 2, 'task'),
       (%s, 'source', '', '', '', '', 'open', 2, 'task');
INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral)
VALUES (%s, 'Rig identity', '', '', '', '', 'open', 1, 'rig', 1);
INSERT INTO wisp_labels (issue_id, label) VALUES (%s, 'gt:rig');
INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
VALUES (%s, %s, %s, 'blocks', NOW(), 'tester', JSON_OBJECT());
INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
VALUES (%s, %s, %s, 'blocks', NOW(), 'tester', JSON_OBJECT());
INSERT INTO wisp_events (id, issue_id, event_type, actor, created_at)
VALUES ('11111111-1111-1111-1111-111111111111', %s, 'created', 'tester', NOW());
INSERT INTO wisp_comments (id, issue_id, author, text, created_at)
VALUES ('22222222-2222-2222-2222-222222222222', %s, 'tester', 'durable identity', NOW());
INSERT INTO wisp_child_counters (parent_id, last_child) VALUES (%s, 7);
`, LatestVersion(),
		doltSQLString(targetID), doltSQLString(sourceID), doltSQLString(rigID),
		doltSQLString(rigID), doltSQLString(depid.New(rigID, targetID)),
		doltSQLString(rigID), doltSQLString(targetID), doltSQLString(depid.New(sourceID, rigID)),
		doltSQLString(sourceID), doltSQLString(rigID), doltSQLString(rigID),
		doltSQLString(rigID), doltSQLString(rigID))
	migrationSQL, err := mainSource.files.ReadFile("migrations/0053_repair_rig_wisps.up.sql")
	if err != nil {
		t.Fatalf("read 0053 migration: %v", err)
	}
	runDoltSQL(t, dir, seedSQL+"\n"+cliCompatibleMigrationSQL("0053_repair_rig_wisps.up.sql", string(migrationSQL)))

	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM issues WHERE id = 'schema-cli-rig' AND issue_type = 'rig' AND ephemeral = 0`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM wisps WHERE id = 'schema-cli-rig'`, "0")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM labels WHERE issue_id = 'schema-cli-rig' AND label = 'gt:rig'`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM dependencies WHERE issue_id = 'schema-cli-rig' AND depends_on_issue_id = 'schema-cli-target'`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM dependencies WHERE issue_id = 'schema-cli-source' AND depends_on_issue_id = 'schema-cli-rig' AND depends_on_wisp_id IS NULL`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM comments WHERE issue_id = 'schema-cli-rig'`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM child_counters WHERE parent_id = 'schema-cli-rig' AND last_child = 7`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM wisp_child_counters WHERE parent_id = 'schema-cli-rig'`, "0")
}

func TestMigration0047RepairsMissingWispTablesThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "mixed-blocked-repair-no-wisps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create no-wisps dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	// Reproduce #4695/#4176: a clone whose main cursor arrives at 0047 with
	// the (dolt_ignore'd, never-synced) wisp tables entirely absent. Seed a
	// normal issue-to-issue block edge so the recompute has real work to do.
	const blockerID = "schema-cli-0047-blocker"
	const blockedID = "schema-cli-0047-blocked"
	seedSQL := fmt.Sprintf(`
DROP TABLE IF EXISTS wisp_child_counters;
DROP TABLE IF EXISTS wisp_comments;
DROP TABLE IF EXISTS wisp_events;
DROP TABLE IF EXISTS wisp_dependencies;
DROP TABLE IF EXISTS wisp_labels;
DROP TABLE IF EXISTS wisps;
DELETE FROM schema_migrations WHERE version = 47;
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES (%s, 'blocker', '', '', '', '', 'open', 2, 'task'),
       (%s, 'blocked', '', '', '', '', 'open', 2, 'task');
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
VALUES (%s, %s, %s, 'blocks', NOW(), 'tester', JSON_OBJECT());
`,
		doltSQLString(blockerID), doltSQLString(blockedID),
		doltSQLString(depid.New(blockedID, blockerID)), doltSQLString(blockedID), doltSQLString(blockerID))

	migrationSQL, err := mainSource.files.ReadFile("migrations/0047_recompute_mixed_is_blocked.up.sql")
	if err != nil {
		t.Fatalf("read 0047 migration: %v", err)
	}
	// The repair DDL that ensureWispTablesForMixedBlockedRecompute issues in
	// Go (plain CREATE TABLE IF NOT EXISTS, no PREPARE) precedes the frozen
	// migration text, exactly mirroring preMigrationRepair's ordering.
	repairSQL := wispsTableDDLForMigration0047 + "\n" + wispDependenciesTableDDLForMigration0047 + "\n"
	runDoltSQL(t, dir, seedSQL+"\n"+repairSQL+string(migrationSQL))

	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'`, "1")
	requireDoltCount(t, dir,
		fmt.Sprintf(`SELECT COUNT(*) AS c FROM issues WHERE id = %s AND is_blocked = 1`, doltSQLString(blockedID)), "1")
	requireDoltCount(t, dir,
		fmt.Sprintf(`SELECT COUNT(*) AS c FROM issues WHERE id = %s AND is_blocked = 0`, doltSQLString(blockerID)), "1")
}

// dependenciesIDRepairSQLForTest mirrors ensureDependenciesIDColumn's Go
// logic (migration_repairs.go) for a single known row, since plain SQL
// cannot reproduce depid.New's UUIDv5 derivation -- CLI tests seed exactly
// one dependency edge and precompute its id in Go. Unlike the real Go
// function, the "is this step already done" checks run here in Go (via a
// live query against dir) rather than as SET/IF/PREPARE dynamic SQL: the
// Dolt CLI does not reliably apply some prepared ALTER TABLE statements
// (see cli_migrations.go's cliCompatibleMigrationSQL doc and its
// per-migration direct-DDL substitutes for exactly this reason), so a
// PREPARE'd ADD COLUMN here is invisible to the very next statement in the
// same dolt sql -f batch. Deciding state in Go and emitting only direct
// (unprepared) DDL sidesteps that CLI limitation while still being safe to
// call more than once against the same database.
func dependenciesIDRepairSQLForTest(t *testing.T, dir, issueID, targetIssueID, precomputedID string) string {
	t.Helper()
	hasID := queryDoltCSV(t, dir, `
		SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'`)[0]["c"] != "0"
	idIsPK := hasID && queryDoltCSV(t, dir, `
		SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id' AND CONSTRAINT_NAME = 'PRIMARY'`)[0]["c"] != "0"

	var b strings.Builder
	if !hasID {
		b.WriteString("ALTER TABLE dependencies ADD COLUMN id CHAR(36) NULL;\n")
		fmt.Fprintf(&b, `UPDATE dependencies SET id = %s
WHERE issue_id = %s
  AND depends_on_issue_id <=> %s
  AND depends_on_wisp_id <=> NULL
  AND depends_on_external <=> NULL
  AND id IS NULL;
`, doltSQLString(precomputedID), doltSQLString(issueID), doltSQLString(targetIssueID))
	}
	if !idIsPK {
		b.WriteString("ALTER TABLE dependencies MODIFY COLUMN id CHAR(36) NOT NULL;\n")
		hasAnyPK := queryDoltCSV(t, dir, `
			SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND CONSTRAINT_TYPE = 'PRIMARY KEY'`)[0]["c"] != "0"
		if hasAnyPK {
			b.WriteString("ALTER TABLE dependencies DROP PRIMARY KEY;\n")
		}
		b.WriteString("ALTER TABLE dependencies ADD PRIMARY KEY (id);\n")
	}
	return b.String()
}

func TestMigration0053NoopsWhenDependenciesMissingIDColumnThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "rig-repair-no-dep-id")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create no-dep-id dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	// Reproduce #4690: dependencies already in split-target/content-derived
	// key shape (the uk_dep_* unique keys on issue_id + typed target, added
	// by 0043, already give it a natural identity), but its surrogate id
	// column was never added at all by this clone's history (a different
	// migration path than this repo's 0043, which both drops the older
	// generated depends_on_id column AND adds id in the same step). Seed one
	// ordinary (non-rig) edge -- zero rig wisps exist, matching the
	// reporter's confirmed-safe-to-no-op precondition -- and drop id,
	// leaving the table keyless (Dolt supports this; the uk_dep_* unique
	// keys still enforce the real identity).
	const sourceID = "schema-cli-0053-source"
	const targetID = "schema-cli-0053-target"
	seedSQL := fmt.Sprintf(`
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES (%s, 'source', '', '', '', '', 'open', 2, 'task'),
       (%s, 'target', '', '', '', '', 'open', 2, 'task');
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
VALUES (%s, %s, %s, 'blocks', NOW(), 'tester', JSON_OBJECT());
ALTER TABLE dependencies DROP PRIMARY KEY;
ALTER TABLE dependencies DROP COLUMN id;
DELETE FROM schema_migrations WHERE version = 53;
`,
		doltSQLString(sourceID), doltSQLString(targetID),
		doltSQLString(depid.New(sourceID, targetID)), doltSQLString(sourceID), doltSQLString(targetID))
	runDoltSQL(t, dir, seedSQL)

	// dependenciesIDRepairSQLForTest mirrors ensureDependenciesIDColumn's Go
	// logic (add the column, backfill, then restore it as the PRIMARY KEY --
	// #2's fix, not just a plain NOT NULL column) for this one known edge.
	repairID := depid.New(sourceID, targetID)
	runDoltSQL(t, dir, dependenciesIDRepairSQLForTest(t, dir, sourceID, targetID, repairID))

	migrationSQL, err := mainSource.files.ReadFile("migrations/0053_repair_rig_wisps.up.sql")
	if err != nil {
		t.Fatalf("read 0053 migration: %v", err)
	}
	runDoltSQL(t, dir, string(migrationSQL))

	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id' AND CONSTRAINT_NAME = 'PRIMARY'`, "1")
	requireDoltCount(t, dir,
		fmt.Sprintf(`SELECT COUNT(*) AS c FROM dependencies WHERE issue_id = %s AND id = %s`, doltSQLString(sourceID), doltSQLString(repairID)), "1")
}

func TestMigration0057IsIdempotentOnAlreadyLongtextEventsThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "events-longtext-idempotent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create events-longtext-idempotent dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	migrationSQL, err := mainSource.files.ReadFile("migrations/0057_events_value_columns_idempotent_longtext.up.sql")
	if err != nil {
		t.Fatalf("read 0057 migration: %v", err)
	}

	// events.old_value/new_value are already LONGTEXT after the fresh chain
	// (0048's unconditional MODIFY). Re-running 0057's guarded SQL a second
	// time -- simulating any repeat pass through this version -- must take
	// the no-op branch for BOTH columns independently rather than re-issuing
	// MODIFY on either one, which is exactly the drift 0048 itself lacks a
	// guard against (#4353). This does not, and cannot, reproduce a raw
	// re-execution of 0048's own frozen SQL by tooling outside bd's
	// cursor-gated migrate chain; see 0057's header comment for that
	// boundary.
	runDoltSQL(t, dir, string(migrationSQL))
	runDoltSQL(t, dir, string(migrationSQL))

	requireDoltColumnShape(t, dir, "events", "old_value", "longtext", "YES")
	requireDoltColumnShape(t, dir, "events", "new_value", "longtext", "YES")
}

// wispIsBlockedColumnSQLForTest decides, from live DB state, whether
// wisps.is_blocked (added by ignored/0006_add_wisp_is_blocked.up.sql) and its
// index still need adding, and emits direct DDL if so. Same rationale as
// dependenciesIDRepairSQLForTest: 0006's own ADD COLUMN/CREATE INDEX are
// PREPARE'd, and the Dolt CLI does not reliably apply a prepared ALTER TABLE
// in this path, so a fresh wisps table (created by this test's v47 repair
// injection, not through the ignored sequence) never actually gains the
// column before ignored/0007 needs it.
func wispIsBlockedColumnSQLForTest(t *testing.T, dir string) string {
	t.Helper()
	hasColumn := queryDoltCSV(t, dir, `
		SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`)[0]["c"] != "0"
	hasIndex := queryDoltCSV(t, dir, `
		SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`)[0]["c"] != "0"

	var b strings.Builder
	if !hasColumn {
		b.WriteString("ALTER TABLE wisps ADD COLUMN is_blocked TINYINT(1) NOT NULL DEFAULT 0;\n")
	}
	if !hasIndex {
		b.WriteString("CREATE INDEX idx_wisps_is_blocked ON wisps(is_blocked, status);\n")
	}
	return b.String()
}

// TestFullChainFromPreWispsAndMissingDependenciesIDConvergesThroughDoltCLI is
// the acceptance test for the migration-chain-hardening branch: it seeds a
// database simultaneously in BOTH drifted shapes this branch fixes --
// #4695/#4176 (cursor arrives with the wisp tables absent) and #4690
// (dependencies never got its surrogate id column) -- and drives the
// migration chain all the way through the main sequence AND the ignored
// sequence to the latest version, the 0047 -> 0053 -> 0056 -> 0057 -> ignored path
// the narrower per-fix CLI tests above stop short of.
//
// Like every other CLI test in this file, this applies migration SQL text
// directly (via cliCompatibleMigrationSQL, the same substitution
// AllMigrationsSQL() uses) rather than calling schema.MigrateUp: the Dolt
// CLI test path here proves the frozen migration files plus the repair SQL
// converge to the correct end state, not the Go orchestration around them
// (runMigrations' commit atomicity is covered separately, and by a real cgo
// embedded-dolt test: see
// internal/storage/embeddeddolt's TestEmbeddedMigrateRepairedDependenciesIDColumnCommitsAtomicallyWithVersion53_4690).
func TestFullChainFromPreWispsAndMissingDependenciesIDConvergesThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "full-chain-convergence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")

	// Build the schema genuinely bounded at v46 -- the real shape those
	// migrations produce, not a hand-simplified stand-in -- using the same
	// cliCompatibleMigrationSQL substitution AllMigrationsSQL() itself uses.
	const seedCursor = 46
	recordCursorSQL := func(cursorTable, name string, version int, data []byte) string {
		sum := sha256.Sum256(data)
		return fmt.Sprintf("INSERT IGNORE INTO %s (version, content_hash) VALUES (%d, %s);\n",
			cursorTable, version, doltSQLString(hex.EncodeToString(sum[:])))
	}
	var bounded strings.Builder
	bounded.WriteString(mainSource.bootstrapSQL())
	bounded.WriteString(";\n")
	for _, f := range mainSource.list() {
		if f.version > seedCursor {
			continue
		}
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			t.Fatalf("read %s: %v", f.name, err)
		}
		bounded.WriteString(cliCompatibleMigrationSQL(f.name, string(data)))
		bounded.WriteString("\n")
		bounded.WriteString(recordCursorSQL("schema_migrations", f.name, f.version, data))
	}
	runDoltSQL(t, dir, bounded.String())

	// Reproduce #4695/#4176 AND #4690 simultaneously: the wisp tables never
	// synced (dolt_ignore'd, absent even though the cursor is past the
	// migrations that create them), and dependencies lost its surrogate id
	// column (a different historical migration path than 0043).
	const blockerID = "full-chain-blocker"
	const blockedID = "full-chain-blocked"
	depID := depid.New(blockedID, blockerID)
	seedSQL := fmt.Sprintf(`
DROP TABLE IF EXISTS wisp_child_counters;
DROP TABLE IF EXISTS wisp_comments;
DROP TABLE IF EXISTS wisp_events;
DROP TABLE IF EXISTS wisp_dependencies;
DROP TABLE IF EXISTS wisp_labels;
DROP TABLE IF EXISTS wisps;
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES (%s, 'blocker', '', '', '', '', 'open', 2, 'task'),
       (%s, 'blocked', '', '', '', '', 'open', 2, 'task');
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
VALUES (%s, %s, %s, 'blocks', NOW(), 'tester', JSON_OBJECT());
ALTER TABLE dependencies DROP PRIMARY KEY;
ALTER TABLE dependencies DROP COLUMN id;
`,
		doltSQLString(blockerID), doltSQLString(blockedID),
		doltSQLString(depID), doltSQLString(blockedID), doltSQLString(blockerID))
	runDoltSQL(t, dir, seedSQL)

	// Drive the main sequence 47..latest (injecting each affected version's
	// repair immediately before its own frozen SQL, mirroring
	// preMigrationRepair's dispatch), then the ignored sequence 1..latest.
	applyRemainingChain := func() {
		var b strings.Builder
		flush := func() {
			if b.Len() == 0 {
				return
			}
			runDoltSQL(t, dir, b.String())
			b.Reset()
		}
		for _, f := range mainSource.list() {
			if f.version <= seedCursor {
				continue
			}
			switch f.version {
			case 47:
				b.WriteString(wispsTableDDLForMigration0047)
				b.WriteString("\n")
				b.WriteString(wispDependenciesTableDDLForMigration0047)
				b.WriteString("\n")
			case 53:
				// dependenciesIDRepairSQLForTest queries live DB state, so
				// the batch built so far must actually be applied first --
				// it decides "hasID"/"idIsPK" from what's really in dir, not
				// from statements still sitting unexecuted in b.
				flush()
				b.WriteString(dependenciesIDRepairSQLForTest(t, dir, blockedID, blockerID, depID))
				b.WriteString("\n")
			}
			data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
			if err != nil {
				t.Fatalf("read %s: %v", f.name, err)
			}
			if f.version == 53 {
				// The registered CLI substitute (cliMigration0053RepairRigWisps)
				// assumes every wisp_* table already exists -- true for a
				// fresh AllMigrationsSQL() bundle, where the ignored sequence's
				// final committed shape is all that matters, but not here:
				// this test's ordering deliberately matches real MigrateUp
				// (main before ignored), so at this point only wisps and
				// wisp_dependencies exist yet. The raw frozen text's own
				// @has_wisps/@has_wisp_labels/... guards handle that correctly
				// (proven already by TestMigration0053NoopsWithoutWispTablesThroughDoltCLI),
				// so use it unsubstituted here.
				b.WriteString(string(data))
			} else {
				b.WriteString(cliCompatibleMigrationSQL(f.name, string(data)))
			}
			b.WriteString("\n")
			b.WriteString(recordCursorSQL("schema_migrations", f.name, f.version, data))
		}
		b.WriteString(ignoredSource.bootstrapSQL())
		b.WriteString(";\n")
		for _, f := range ignoredSource.list() {
			if f.name == "0006_add_wisp_is_blocked.up.sql" {
				// Same Dolt-CLI prepared-ALTER limitation as the v53 repair
				// above: 0006's ADD COLUMN/CREATE INDEX are PREPARE'd, and a
				// prepared ALTER TABLE is not reliably applied through this
				// path (cli_migrations.go), so 0007 (which needs
				// wisps.is_blocked to already exist) fails right after it.
				// Decide state in Go and emit direct DDL instead, same
				// technique as dependenciesIDRepairSQLForTest.
				flush()
				b.WriteString(wispIsBlockedColumnSQLForTest(t, dir))
				b.WriteString("\n")
			}
			data, err := ignoredSource.files.ReadFile(ignoredSource.dir + "/" + f.name)
			if err != nil {
				t.Fatalf("read ignored %s: %v", f.name, err)
			}
			b.WriteString(cliCompatibleMigrationSQL(f.name, string(data)))
			b.WriteString("\n")
			b.WriteString(recordCursorSQL("ignored_schema_migrations", f.name, f.version, data))
		}
		flush()
	}

	applyRemainingChain()

	assertConverged := func() {
		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'`, "1")
		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'`, "1")
		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'no_history'`, "1")
		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'started_at'`, "1")
		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND INDEX_NAME = 'idx_wisp_dep_type'`, "1")

		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'`, "1")
		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id' AND CONSTRAINT_NAME = 'PRIMARY'`, "1")
		requireDoltCount(t, dir,
			`SELECT COUNT(*) AS c FROM dependencies WHERE id IS NULL`, "0")
		requireDoltCount(t, dir,
			fmt.Sprintf(`SELECT COUNT(*) AS c FROM dependencies WHERE issue_id = %s AND id = %s`, doltSQLString(blockedID), doltSQLString(depID)), "1")
		requireDoltCount(t, dir, `SELECT COUNT(*) AS c FROM dependencies`, "1")

		requireDoltCount(t, dir,
			fmt.Sprintf(`SELECT COUNT(*) AS c FROM issues WHERE id = %s AND is_blocked = 1`, doltSQLString(blockedID)), "1")
		requireDoltCount(t, dir,
			fmt.Sprintf(`SELECT COUNT(*) AS c FROM issues WHERE id = %s AND is_blocked = 0`, doltSQLString(blockerID)), "1")

		requireDoltCount(t, dir,
			fmt.Sprintf(`SELECT COUNT(*) AS c FROM schema_migrations WHERE version = %d`, LatestVersion()), "1")
		requireDoltCount(t, dir,
			fmt.Sprintf(`SELECT COUNT(*) AS c FROM ignored_schema_migrations WHERE version = %d`, LatestIgnoredVersion()), "1")
	}
	assertConverged()

	// Re-open: a real second schema.MigrateUp on this database would see
	// both cursors already at latest and do nothing at all -- this harness
	// applies raw migration SQL text directly (see the package doc comment
	// above) rather than calling MigrateUp, so it has no cursor gate to
	// reproduce that no-op with. Re-running every migration file
	// unconditionally is NOT an equivalent stand-in: some of the
	// cliCompatibleMigrationSQL substitutes this test also relies on (0054's
	// and 0055's direct-DDL lease-column bundles, in particular) are
	// deliberately single-application fresh-bundle DDL, not idempotent, and
	// re-running them is a false failure orthogonal to this branch's fixes.
	//
	// What a re-open must prove for THIS branch is narrower and more
	// precise: (1) both repair-detection helpers, queried fresh against live
	// state, find nothing left to do -- proving ensureDependenciesIDColumn's
	// and the wisp-tables repair's target state is fully converged, not just
	// "ran once and happened to look right"; and (2) the three frozen
	// migration files this branch's fixes actually touch (0047, 0053, 0057)
	// are safe to re-run on the now-converged database without error or
	// drift, which their own guards (not this test) are responsible for.
	if repair := dependenciesIDRepairSQLForTest(t, dir, blockedID, blockerID, depID); repair != "" {
		t.Fatalf("dependencies id repair has more work after convergence, want none:\n%s", repair)
	}
	if repair := wispIsBlockedColumnSQLForTest(t, dir); repair != "" {
		t.Fatalf("wisps.is_blocked repair has more work after convergence, want none:\n%s", repair)
	}
	for _, name := range []string{
		"0047_recompute_mixed_is_blocked.up.sql",
		"0053_repair_rig_wisps.up.sql",
		"0057_events_value_columns_idempotent_longtext.up.sql",
	} {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		runDoltSQL(t, dir, string(data))
	}
	assertConverged()
}

func TestWispDependenciesSplitTargetBackfillPrefersWispOverIssueThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "wisp-dependency-split")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create wisp dependency split dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")

	var repairSQL strings.Builder
	for _, col := range wispDependenciesSplitTargetColumns() {
		fmt.Fprintf(&repairSQL, "ALTER TABLE wisp_dependencies ADD COLUMN %s %s;\n", col.name, col.definition)
	}
	for _, stmt := range wispDependenciesSplitTargetBackfillSQL() {
		repairSQL.WriteString(stmt)
		repairSQL.WriteString(";\n")
	}

	const sourceID = "source-wisp"
	const ambiguousID = "ambiguous-target"
	seedSQL := fmt.Sprintf(`
CREATE TABLE issues (
    id VARCHAR(255) PRIMARY KEY
);
CREATE TABLE wisps (
    id VARCHAR(255) PRIMARY KEY
);
CREATE TABLE wisp_dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, depends_on_id)
);
INSERT INTO issues (id) VALUES (%s);
INSERT INTO wisps (id) VALUES (%s), (%s);
INSERT INTO wisp_dependencies (issue_id, depends_on_id) VALUES (%s, %s);
`, doltSQLString(ambiguousID),
		doltSQLString(sourceID), doltSQLString(ambiguousID),
		doltSQLString(sourceID), doltSQLString(ambiguousID))
	runDoltSQL(t, dir, seedSQL+repairSQL.String())

	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM wisp_dependencies WHERE issue_id = 'source-wisp' AND depends_on_wisp_id = 'ambiguous-target' AND depends_on_issue_id IS NULL AND depends_on_external IS NULL`, "1")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM wisp_dependencies WHERE issue_id = 'source-wisp' AND depends_on_issue_id = 'ambiguous-target'`, "0")
}

func TestMigration0047HandlesLegacyWispDependenciesShape(t *testing.T) {
	sql, err := os.ReadFile("migrations/0047_recompute_mixed_is_blocked.up.sql")
	if err != nil {
		t.Fatalf("read 0047 up migration: %v", err)
	}

	body := string(sql)
	for _, want := range []string{
		"@wisp_dependencies_needs_split",
		"ALTER TABLE wisp_dependencies ADD COLUMN depends_on_issue_id",
		"ALTER TABLE wisp_dependencies ADD COLUMN depends_on_wisp_id",
		"ALTER TABLE wisp_dependencies ADD COLUMN depends_on_id VARCHAR(255) AS",
		"cd.depends_on_issue_id",
		"d.depends_on_wisp_id",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("0047 migration missing legacy wisp_dependencies compatibility marker %q", want)
		}
	}
}

func TestCLICompatibleMigration0046UsesFreshSchemaDDLOnly(t *testing.T) {
	got := cliCompatibleMigrationSQL("0046_add_is_blocked.up.sql", "source migration")
	for _, want := range []string{
		"ALTER TABLE issues ADD COLUMN is_blocked TINYINT(1) NOT NULL DEFAULT 0",
		"CREATE INDEX idx_issues_is_blocked ON issues(is_blocked, status)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("0046 CLI migration missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"UPDATE issues",
		"WITH RECURSIVE",
		"directly_blocked",
		"recursively_blocked",
		"parent-child",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("0046 CLI migration contains dead backfill marker %q", forbidden)
		}
	}
}

func TestCLICompatibleMigration0008MatchesRuntimeChildCountersFK(t *testing.T) {
	got := cliCompatibleMigrationSQL("0008_create_child_counters.up.sql", "source migration")
	if want := "CONSTRAINT fk_counter_parent FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE"; !strings.Contains(got, want) {
		t.Fatalf("0008 CLI migration missing %q", want)
	}
}

func TestCLICompatibleMigration0032UsesDirectDropColumn(t *testing.T) {
	got := cliCompatibleMigrationSQL("0032_drop_schema_migrations_applied_at.up.sql", "source migration")
	if want := "ALTER TABLE schema_migrations DROP COLUMN applied_at"; !strings.Contains(got, want) {
		t.Fatalf("0032 CLI migration missing %q", want)
	}
	for _, forbidden := range []string{
		"PREPARE",
		"EXECUTE",
		"DEALLOCATE",
		"@needs_drop",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("0032 CLI migration contains prepared-DDL marker %q", forbidden)
		}
	}
}

func TestCLICompatibleMigration0049UsesDirectLongtextDDL(t *testing.T) {
	got := cliCompatibleMigrationSQL("0049_longtext_large_content_columns.up.sql", "source migration")
	for _, want := range []string{
		"ALTER TABLE issues MODIFY COLUMN description LONGTEXT NOT NULL",
		"MODIFY COLUMN design LONGTEXT NOT NULL",
		"MODIFY COLUMN acceptance_criteria LONGTEXT NOT NULL",
		"MODIFY COLUMN notes LONGTEXT NOT NULL",
		"ALTER TABLE issues MODIFY COLUMN close_reason LONGTEXT DEFAULT ''",
		"ALTER TABLE wisps MODIFY COLUMN description LONGTEXT NOT NULL DEFAULT ''",
		"ALTER TABLE wisps MODIFY COLUMN close_reason LONGTEXT DEFAULT ''",
		"ALTER TABLE comments MODIFY COLUMN text LONGTEXT NOT NULL",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("0049 CLI migration missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"PREPARE",
		"EXECUTE",
		"DEALLOCATE",
		"@issues_needs_fix",
		"@comments_needs_fix",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("0049 CLI migration contains prepared-DDL marker %q", forbidden)
		}
	}
}

func TestCLICompatibleMigration0039PreservesRuntimeChildCountersFK(t *testing.T) {
	got := cliCompatibleMigrationSQL("0039_drop_child_counters_fk.up.sql", "source migration")
	if strings.TrimSpace(got) != "SELECT 1;" {
		t.Fatalf("0039 CLI migration = %q, want SELECT 1", got)
	}
}

func TestAllMigrationsSQLUsesDirectDDLForKnownCLIIncompatibilities(t *testing.T) {
	got := AllMigrationsSQL()
	for _, want := range []string{
		"CONSTRAINT fk_counter_parent FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE",
		"ALTER TABLE schema_migrations DROP COLUMN applied_at",
		"ALTER TABLE issues MODIFY COLUMN close_reason LONGTEXT DEFAULT ''",
		"ALTER TABLE comments MODIFY COLUMN text LONGTEXT NOT NULL",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("AllMigrationsSQL missing direct CLI DDL %q", want)
		}
	}
	for _, forbidden := range []string{
		"COLUMN_NAME = 'applied_at'",
		"ALTER TABLE child_counters DROP FOREIGN KEY fk_counter_parent",
		"@issues_cr_needs_fix",
		"@comments_needs_fix",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("AllMigrationsSQL contains source prepared-DDL guard %q", forbidden)
		}
	}
}

func TestAllMigrationsSQLAppliesThroughDoltCLIAndRecordsLatestVersion(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "cli-bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create CLI bundle dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	rows := queryDoltCSV(t, dir, `
SELECT COALESCE(MAX(version), 0) AS max_version, COUNT(*) AS version_count
FROM schema_migrations`)
	if len(rows) != 1 {
		t.Fatalf("schema_migrations query returned %d rows, want 1", len(rows))
	}
	want := strconv.Itoa(LatestVersion())
	if got := rows[0]["max_version"]; got != want {
		t.Fatalf("MAX(version) = %s, want %s", got, want)
	}
	if got := rows[0]["version_count"]; got != want {
		t.Fatalf("COUNT(*) = %s, want %s", got, want)
	}

	requireDoltNoRows(t, dir, `
SELECT column_name
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'schema_migrations'
  AND column_name = 'applied_at'`, "schema_migrations.applied_at")
	requireDoltFKRules(t, dir, "fk_comments_issue", "CASCADE", "CASCADE")
	requireDoltColumnShape(t, dir, "comments", "text", "longtext", "NO")
	requireDoltColumnShape(t, dir, "issues", "description", "longtext", "NO")
	requireDoltColumnShape(t, dir, "wisps", "description", "longtext", "NO")
	requireDoltColumnShape(t, dir, "wisps", "no_history", "tinyint(1)", "YES")
	requireDoltColumnShape(t, dir, "wisps", "started_at", "datetime", "YES")
	requireDoltColumnShape(t, dir, "wisps", "wisp_type", "varchar(32)", "YES")
}

func runDoltCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("dolt", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt %v failed in %s: %v\nOutput: %s", args, dir, err, output)
	}
}

func runDoltSQL(t *testing.T, dir, query string) {
	t.Helper()
	sqlFile := filepath.Join(t.TempDir(), "migration-bundle.sql")
	if err := os.WriteFile(sqlFile, []byte(query), 0o644); err != nil {
		t.Fatalf("write dolt sql file: %v", err)
	}
	runDoltCommand(t, dir, "sql", "-f", sqlFile)
}

func queryDoltCSV(t *testing.T, dir, query string) []map[string]string {
	t.Helper()
	cmd := exec.Command("dolt", "sql", "-q", query, "-r", "csv")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt sql query failed in %s: %v\nQuery: %s\nOutput: %s", dir, err, query, output)
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil
	}
	records, err := csv.NewReader(strings.NewReader(trimmed)).ReadAll()
	if err != nil {
		t.Fatalf("parse dolt CSV output: %v\nRaw: %s", err, output)
	}
	if len(records) < 2 {
		return nil
	}
	headers := records[0]
	rows := make([]map[string]string, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make(map[string]string, len(headers))
		for i, header := range headers {
			if i < len(record) {
				row[header] = record[i]
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func requireDoltNoRows(t *testing.T, dir, query, subject string) {
	t.Helper()
	if rows := queryDoltCSV(t, dir, query); len(rows) != 0 {
		t.Fatalf("%s query returned %d rows, want none: %v", subject, len(rows), rows)
	}
}

func requireDoltCount(t *testing.T, dir, query, want string) {
	t.Helper()
	rows := queryDoltCSV(t, dir, query)
	if len(rows) != 1 {
		t.Fatalf("count query returned %d rows, want 1: %v", len(rows), rows)
	}
	if got := rows[0]["c"]; got != want {
		t.Fatalf("count query returned %s, want %s\nQuery: %s", got, want, query)
	}
}

func requireDoltFKRules(t *testing.T, dir, constraintName, wantUpdate, wantDelete string) {
	t.Helper()
	rows := queryDoltCSV(t, dir, fmt.Sprintf(`
SELECT update_rule AS update_rule, delete_rule AS delete_rule
FROM information_schema.referential_constraints
WHERE constraint_schema = DATABASE()
  AND constraint_name = %s`, doltSQLString(constraintName)))
	if len(rows) != 1 {
		t.Fatalf("%s FK query returned %d rows, want 1: %v", constraintName, len(rows), rows)
	}
	if got := rows[0]["update_rule"]; got != wantUpdate {
		t.Fatalf("%s UPDATE_RULE = %s, want %s", constraintName, got, wantUpdate)
	}
	if got := rows[0]["delete_rule"]; got != wantDelete {
		t.Fatalf("%s DELETE_RULE = %s, want %s", constraintName, got, wantDelete)
	}
}

func requireDoltColumnShape(t *testing.T, dir, tableName, columnName, wantType, wantNullable string) {
	t.Helper()
	rows := queryDoltCSV(t, dir, fmt.Sprintf(`
SELECT column_type AS column_type, is_nullable AS is_nullable
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = %s
  AND column_name = %s`, doltSQLString(tableName), doltSQLString(columnName)))
	if len(rows) != 1 {
		t.Fatalf("%s.%s column query returned %d rows, want 1: %v", tableName, columnName, len(rows), rows)
	}
	if got := rows[0]["column_type"]; got != wantType {
		t.Fatalf("%s.%s COLUMN_TYPE = %s, want %s", tableName, columnName, got, wantType)
	}
	if got := rows[0]["is_nullable"]; got != wantNullable {
		t.Fatalf("%s.%s IS_NULLABLE = %s, want %s", tableName, columnName, got, wantNullable)
	}
}

func doltSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func TestStageSchemaTablesSkipsIgnoredTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s\s+WHERE NOT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).
			AddRow("schema_migrations", false))
	mock.ExpectQuery(`(?s)SELECT t\.TABLE_NAME\s+FROM INFORMATION_SCHEMA\.TABLES t\s+WHERE .*NOT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME"}).
			AddRow("schema_migrations"))
	mock.ExpectExec(`CALL DOLT_ADD\('-f', \?\)`).
		WithArgs("schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 1))

	staged, err := stageSchemaTables(context.Background(), db, map[string]dirtyTableState{})
	if err != nil {
		t.Fatalf("stageSchemaTables: %v", err)
	}
	if !staged {
		t.Fatal("stageSchemaTables staged = false, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUnstageIgnoredTablesResetsExistingIgnoredTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s\s+WHERE EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).
			AddRow("ignored_schema_migrations", true).
			AddRow("wisp_dependencies", true).
			AddRow("wisps", false))
	mock.ExpectExec(`CALL DOLT_RESET\(\?\)`).
		WithArgs("ignored_schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`CALL DOLT_RESET\(\?\)`).
		WithArgs("wisp_dependencies").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := unstageIgnoredTables(context.Background(), db); err != nil {
		t.Fatalf("unstageIgnoredTables: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// showColumnsRows builds a SHOW COLUMNS result with one row per supplied field
// name, mirroring the Field/Type/Null/Key/Default/Extra shape Dolt returns.
func showColumnsRows(fields ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, f := range fields {
		rows.AddRow(f, "char(64)", "YES", "", nil, "")
	}
	return rows
}

func TestHasContentHashColumnUsesShowColumns(t *testing.T) {
	t.Run("column present", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
			WillReturnRows(showColumnsRows("content_hash"))

		has, err := mainSource.hasContentHashColumn(context.Background(), db)
		if err != nil {
			t.Fatalf("hasContentHashColumn: %v", err)
		}
		if !has {
			t.Fatal("has = false, want true when SHOW COLUMNS returns the column")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("column absent", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		defer db.Close()
		mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations`).
			WillReturnRows(showColumnsRows())

		has, err := mainSource.hasContentHashColumn(context.Background(), db)
		if err != nil {
			t.Fatalf("hasContentHashColumn: %v", err)
		}
		if has {
			t.Fatal("has = true, want false when SHOW COLUMNS returns no rows")
		}
	})

	t.Run("missing table reports false without error", func(t *testing.T) {
		// The old INFORMATION_SCHEMA probe returned count 0 for an absent table;
		// SHOW COLUMNS errors with 1146 instead. That error must be swallowed so a
		// not-yet-created cursor table still reports "no content_hash column".
		db, mock, _ := sqlmock.New()
		defer db.Close()
		mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations`).
			WillReturnError(errors.New("Error 1146: Table 'beads.schema_migrations' doesn't exist"))

		has, err := mainSource.hasContentHashColumn(context.Background(), db)
		if err != nil {
			t.Fatalf("hasContentHashColumn returned error for missing table, want nil: %v", err)
		}
		if has {
			t.Fatal("has = true, want false for a missing cursor table")
		}
	})

	t.Run("non-matching field is rejected", func(t *testing.T) {
		// '_' is a LIKE single-char wildcard, so 'contentXhash' could slip past
		// the server-side filter; the exact Field comparison must reject it.
		db, mock, _ := sqlmock.New()
		defer db.Close()
		mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations`).
			WillReturnRows(showColumnsRows("contentXhash"))

		has, err := mainSource.hasContentHashColumn(context.Background(), db)
		if err != nil {
			t.Fatalf("hasContentHashColumn: %v", err)
		}
		if has {
			t.Fatal("has = true, want false for a column that only matches the LIKE wildcard")
		}
	})

	t.Run("propagates unexpected errors", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		defer db.Close()
		mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations`).
			WillReturnError(errors.New("connection refused"))

		if _, err := mainSource.hasContentHashColumn(context.Background(), db); err == nil {
			t.Fatal("expected unexpected error to propagate, got nil")
		}
	})
}

// TestHasContentHashColumnMatchesInformationSchemaOnDolt proves on a real Dolt
// database that the SHOW COLUMNS probe returns the same answer as the retired
// INFORMATION_SCHEMA.COLUMNS probe, in both the present and absent states.
func TestHasContentHashColumnMatchesInformationSchemaOnDolt(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "content-hash-probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create probe dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")

	const table = "schema_migrations"

	// The retired probe: COUNT(*) over INFORMATION_SCHEMA.COLUMNS.
	infoSchemaHas := func() bool {
		rows := queryDoltCSV(t, dir, fmt.Sprintf(`
SELECT COUNT(*) AS cnt
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND COLUMN_NAME = 'content_hash'`, table))
		return len(rows) == 1 && rows[0]["cnt"] == "1"
	}
	// The new probe: SHOW COLUMNS ... LIKE, matching the Field name exactly.
	showColumnsHas := func() bool {
		rows := queryDoltCSV(t, dir, fmt.Sprintf("SHOW COLUMNS FROM %s LIKE 'content_hash'", table))
		for _, r := range rows {
			for _, v := range r {
				if v == "content_hash" {
					return true
				}
			}
		}
		return false
	}

	// State 1: cursor table carries content_hash (matches bootstrapSQL).
	runDoltSQL(t, dir, fmt.Sprintf(
		"CREATE TABLE %s (version INT PRIMARY KEY, applied_at DATETIME, content_hash CHAR(64))", table))
	if !showColumnsHas() {
		t.Fatal("SHOW COLUMNS reported no content_hash on a table that has it")
	}
	if got, want := showColumnsHas(), infoSchemaHas(); got != want {
		t.Fatalf("with content_hash: SHOW COLUMNS=%v, INFORMATION_SCHEMA=%v", got, want)
	}

	// State 2: same table without content_hash.
	runDoltSQL(t, dir, fmt.Sprintf("ALTER TABLE %s DROP COLUMN content_hash", table))
	if showColumnsHas() {
		t.Fatal("SHOW COLUMNS reported content_hash on a table that lacks it")
	}
	if got, want := showColumnsHas(), infoSchemaHas(); got != want {
		t.Fatalf("without content_hash: SHOW COLUMNS=%v, INFORMATION_SCHEMA=%v", got, want)
	}
}

type mockDB struct{}

func (m *mockDB) ExecContext(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, nil
}

func (m *mockDB) QueryContext(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	panic("not called")
}

func (m *mockDB) QueryRowContext(_ context.Context, _ string, _ ...any) *sql.Row {
	panic("not called")
}

func TestRunMigrationsStderrOutput(t *testing.T) {
	var buf bytes.Buffer
	orig := stderr
	stderr = &buf
	defer func() { stderr = orig }()

	// mockDB.QueryRowContext panics, so stub issueRowCounter (be-8ja's
	// large-rig check, which now runs unconditionally at the top of
	// runMigrations) to report a small rig — no warning line, so this test's
	// line-count assertion stays scoped to the per-migration progress lines.
	origCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	defer func() { issueRowCounter = origCounter }()

	// Bounded below migration 47: that version's preMigrationRepair (and 53's)
	// issues real INFORMATION_SCHEMA probes (see
	// TestPreMigrationRepairScopedToMain0047 /
	// TestPreMigrationRepairScopedToMain0053), which mockDB.QueryRowContext
	// doesn't support. This test only exercises the stderr lines, not the
	// repair path.
	n, err := runMigrations(context.Background(), &mockDB{}, mainSource, 0, 46, false)
	if err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if n == 0 {
		t.Fatal("expected at least one migration to run")
	}

	got := buf.String()
	if !strings.Contains(got, "Applying migration ") {
		t.Errorf("expected stderr to contain 'Applying migration ', got: %q", got)
	}
	if !strings.Contains(got, "  done (") {
		t.Errorf("expected stderr to contain a '  done (Ns)' timing line, got: %q", got)
	}
	// One "Applying" line + one "done" line per migration.
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2*n {
		t.Errorf("expected %d stderr lines (2 per migration), got %d", 2*n, len(lines))
	}
}

// TestRunMigrationsUsesProvidedSource verifies that runMigrations operates on
// the supplied migrationSource rather than always falling back to mainSource.
// Regression test for the bug where ignoredSource.migrate() silently ran main
// migrations and left ignored_schema_migrations empty (no wisp tables).
func TestRunMigrationsUsesProvidedSource(t *testing.T) {
	orig := stderr
	stderr = &bytes.Buffer{}
	defer func() { stderr = orig }()

	// Stub the large-rig row counter for the same reason as
	// TestRunMigrationsStderrOutput: mockDB.QueryRowContext panics.
	origCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	defer func() { issueRowCounter = origCounter }()

	// Bounded below migration 47 for the same reason as
	// TestRunMigrationsStderrOutput: mockDB can't answer the real
	// INFORMATION_SCHEMA queries preMigrationRepair issues from that version
	// (and from 53) onward.
	main, err := runMigrations(context.Background(), &mockDB{}, mainSource, 0, 46, false)
	if err != nil {
		t.Fatalf("runMigrations(mainSource): %v", err)
	}
	// Same upTo cap as the mainSource call: the source-threading regression
	// this test guards (hardcoding mainSource) is only detectable when both
	// calls share a bound, so their counts collapse to equal under the bug.
	// ignoredSource has 11 migrations, so 46 is a no-op on correct behavior.
	ignored, err := runMigrations(context.Background(), &mockDB{}, ignoredSource, 0, 46, false)
	if err != nil {
		t.Fatalf("runMigrations(ignoredSource): %v", err)
	}
	if main == 0 || ignored == 0 {
		t.Fatalf("expected non-zero counts; main=%d ignored=%d", main, ignored)
	}
	if main == ignored {
		t.Errorf("runMigrations ignored its source argument: main and ignored both returned %d", main)
	}
}

// TestRunMigrationsLargeRigNoticeOnlyOnMainSource pins the round-2 review
// fix: MigrateUp calls runMigrations once for mainSource and once for
// ignoredSource in the same pass (schema.go's MigrateUp). Without gating the
// large-rig notice to the main-source pass, a large rig with pending
// migrations in both sources would print the "one-shot" warning twice (and
// issue the COUNT(*) query twice). The notice must fire on the mainSource
// call and stay silent on the ignoredSource call.
func TestRunMigrationsLargeRigNoticeOnlyOnMainSource(t *testing.T) {
	origCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 49_187, nil }
	defer func() { issueRowCounter = origCounter }()

	var mainBuf bytes.Buffer
	orig := stderr
	stderr = &mainBuf
	if _, err := runMigrations(context.Background(), &mockDB{}, mainSource, 0, 46, false); err != nil {
		stderr = orig
		t.Fatalf("runMigrations(mainSource): %v", err)
	}
	if !strings.Contains(mainBuf.String(), "Large rig detected") {
		stderr = orig
		t.Errorf("expected mainSource pass to emit the large-rig notice; got: %q", mainBuf.String())
	}

	var ignoredBuf bytes.Buffer
	stderr = &ignoredBuf
	_, err := runMigrations(context.Background(), &mockDB{}, ignoredSource, 0, 46, false)
	stderr = orig
	if err != nil {
		t.Fatalf("runMigrations(ignoredSource): %v", err)
	}
	if strings.Contains(ignoredBuf.String(), "Large rig detected") {
		t.Errorf("expected ignoredSource pass to stay silent on the large-rig notice (main-source pass already warned); got: %q", ignoredBuf.String())
	}
}
