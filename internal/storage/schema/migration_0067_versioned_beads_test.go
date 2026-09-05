package schema

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// Phase 1 of the versioned-beads epic (be-hs42e / gastownhall/beads#6132,
// this slice: be-hs42e.2 / #6134) adds purely additive schema: a per-issue
// version log, a store-global epoch singleton, and a fast-path revision
// column on issues. Nothing reads or writes these surfaces yet — that is
// Phase 2 (#6135) and Phase 3 (#6136). These tests only assert the DDL
// itself: shape, emptiness, and that old, unmodified callers of `issues`
// are unaffected.

const migration0067Up = "0067_add_versioned_beads_schema.up.sql"
const migration0067Down = "0067_add_versioned_beads_schema.down.sql"

// TestLatestVersionIncludesMigration0067 pins the real next free slot this
// phase claims. It is deliberately a hardcoded literal, not a comparison
// against another derived value: schema.LatestVersion() drifting to 67 for
// the wrong reason (an unrelated migration landing first) should still be
// caught by this test failing to explain why 67 is versioned-beads-shaped,
// which the CLI test below checks.
func TestLatestVersionIncludesMigration0067(t *testing.T) {
	const want = 67
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (issue_versions/store_epoch/issues.current_revision migration slot claimed by be-hs42e.2)", got, want)
	}
}

// TestMigration0067AddsVersionedBeadsSchema is a pure-Go, DB-independent
// check of the frozen migration bytes themselves — it runs even where no
// `dolt` binary is available.
//
// It pins the shape both ADD COLUMNs must keep: an INFORMATION_SCHEMA-guarded
// PREPARE, on both planes. That guard is not decoration — it is what makes a
// raw replay of this file onto an already-migrated store a no-op, which
// internal/storage/dolt's pr4107 replay harness requires of every migration
// >= 0046 (it re-executes the .up.sql bytes directly, so no Go-side guard is
// in the path). Dolt accepts no unprepared conditional DDL that would do the
// same job: ADD COLUMN IF NOT EXISTS, DROP COLUMN IF EXISTS and a top-level
// IF are all parse errors, and a stored procedure's body needs a client-side
// DELIMITER the CLI batch path requires and the Go driver rejects.
//
// The cost is the pre-2.3 CLI hazard a PREPARE'd ADD COLUMN carries (it
// silently vanishes on the CLI-bundle path — see cli_prepared_ddl.go), and
// the fix for that is the same one 0060/0065/0066 use: a direct-DDL override
// in cliCompatibleMigrationSQL, which
// TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified and
// TestAllMigrationsSQLUsesDirectDDLForKnownCLIIncompatibilities both police.
// The assertion below is the local anchor for that pairing: if someone ever
// unwraps these PREPAREs, the override silently becomes a lie.
func TestMigration0067AddsVersionedBeadsSchema(t *testing.T) {
	upSQL, err := MigrationSQL(migration0067Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0067Up, err)
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS issue_versions",
		"CREATE TABLE IF NOT EXISTS store_epoch",
		"PRIMARY KEY (issue_id, revision)",
		"ALTER TABLE issues ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1",
		// Shape parity with issues, identical type. Nothing reads or writes
		// this column in any phase — see the migration header's asymmetry
		// note and ignored/0026.
		"ALTER TABLE wisps ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1",
		// Both ALTERs are INFORMATION_SCHEMA-guarded so a raw replay no-ops.
		"COLUMN_NAME = 'current_revision'",
		"@issues_cr_needs_add",
		"@wisps_cr_needs_add",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0067 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0067 up migration must keep its guarded PREPARE blocks — they are what make a raw .up.sql replay onto an already-migrated store a no-op (pr4107_corruption_test.go's harness bypasses every Go-side guard), and Dolt accepts no unprepared conditional ADD COLUMN. Unwrapping them also invalidates cliMigration0067AddVersionedBeadsSchema.")
	}
	// The bundle override is what keeps the PREPARE above off the pre-2.3
	// CLI path. Assert it directly rather than trusting the two schema_test
	// assertions to stay pointed at this migration.
	for _, want := range []string{
		"ALTER TABLE issues ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1;",
		"ALTER TABLE wisps ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1;",
	} {
		if !strings.Contains(cliCompatibleMigrationSQL(migration0067Up, upSQL), want) {
			t.Errorf("0067's CLI bundle substitute missing direct DDL %q", want)
		}
	}
	if !cliSubstituteAssumesWispTables(migration0067Up) {
		t.Error("0067's CLI substitute ALTERs wisps unguarded, so it must be listed in cliSubstituteAssumesWispTables — a replay over a clone that never synced the wisp tables has to fall back to the frozen source text")
	}
	if strings.Contains(upSQL, "INSERT INTO store_epoch") || strings.Contains(upSQL, "INSERT IGNORE INTO store_epoch") {
		t.Error("0067 up migration must not seed store_epoch — every new table starts empty in Phase 1 (no reads/writes of the new surfaces until Phase 2)")
	}

	// down.sql files are not part of the embedded FS (only migrations/*.up.sql
	// is //go:embed'd — see mainSource.files), so unlike the up side above,
	// this reads straight from disk by package-relative path, matching
	// TestMigration0035HandlesLegacyWispDependenciesShape's precedent.
	downBytes, err := os.ReadFile("migrations/" + migration0067Down)
	if err != nil {
		t.Fatalf("read %s: %v, want the migration file to exist", migration0067Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"DROP TABLE",
		"issue_versions",
		"store_epoch",
		"ALTER TABLE issues DROP COLUMN current_revision",
		// The down side reverses both planes, or a rolled-back workspace
		// keeps a wisps column its binary no longer knows about.
		"ALTER TABLE wisps DROP COLUMN current_revision",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0067 down migration missing %q\nfull SQL:\n%s", want, downSQL)
		}
	}
	// Only migrations/*.up.sql is embedded into the CLI fresh bundle
	// (mainSource.files), so the pre-2.3 prepared-DDL hazard never reaches a
	// down migration and the guard is free — 0060's down is the precedent.
	if !strings.Contains(strings.ToUpper(downSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0067 down migration must guard its DROP COLUMNs the way the up migration guards its ADD COLUMNs, so an issues-only or partially-applied workspace rolls back as safely as it migrated up")
	}
}

// TestMigration0067AddsVersionedBeadsSchemaThroughDoltCLI applies the full
// migration bundle through a real `dolt` binary (skipped without one — see
// testutil.RequireDoltBinary) and checks the shape acceptance criteria a
// pure-Go SQL-text check cannot: actual column types/nullability as Dolt
// reports them, that both new tables start empty, that the composite and
// singleton primary keys are enforced, and compat direction 1 — an
// old-shaped INSERT/SELECT against `issues` that has never heard of
// current_revision still behaves unchanged, because the column is
// NOT NULL DEFAULT 1 rather than a value old callers must supply.
func TestMigration0067AddsVersionedBeadsSchemaThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := t.TempDir()
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	// issue_versions shape.
	requireDoltColumnShape(t, dir, "issue_versions", "issue_id", "varchar(255)", "NO")
	requireDoltDataType(t, dir, "issue_versions", "revision", "bigint", "NO")
	requireDoltDataType(t, dir, "issue_versions", "epoch", "int", "NO")
	requireDoltDataType(t, dir, "issue_versions", "durable_state", "json", "YES")
	requireDoltColumnShape(t, dir, "issue_versions", "change_actor", "varchar(255)", "YES")
	requireDoltColumnShape(t, dir, "issue_versions", "change_agent", "varchar(255)", "YES")
	requireDoltColumnShape(t, dir, "issue_versions", "change_message", "text", "YES")
	requireDoltColumnShape(t, dir, "issue_versions", "change_at", "datetime", "NO")
	requireDoltColumnShape(t, dir, "issue_versions", "removed_at", "datetime", "YES")
	requireDoltColumnShape(t, dir, "issue_versions", "removed_reason", "varchar(255)", "YES")
	requireDoltNoRows(t, dir, "SELECT issue_id FROM issue_versions", "issue_versions")

	// store_epoch shape — singleton table, starts empty (Phase 1 does not
	// seed it; a later phase's lazy-init owns creating the id=1 row).
	requireDoltColumnShape(t, dir, "store_epoch", "id", "tinyint(1)", "NO")
	requireDoltDataType(t, dir, "store_epoch", "epoch", "int", "NO")
	requireDoltColumnShape(t, dir, "store_epoch", "bumped_at", "datetime", "YES")
	requireDoltColumnShape(t, dir, "store_epoch", "bumped_reason", "varchar(255)", "YES")
	requireDoltNoRows(t, dir, "SELECT id FROM store_epoch", "store_epoch")

	// current_revision on BOTH planes. issues.current_revision is the
	// fast-path CAS column later phases read and write; wisps.current_revision
	// is shape parity only (nothing ever reads or writes it — see the
	// migration header and ignored/0026), and it is asserted here for exactly
	// the reason the parity oracle exists: the two planes share
	// column-list-shaped code paths, so the shape obligation is real even
	// where the semantics are not. Same type on both, or
	// TestSchemaParityIssuesVsWisps' type check fails instead of its
	// name check.
	requireDoltDataType(t, dir, "issues", "current_revision", "bigint", "NO")
	requireDoltDataType(t, dir, "wisps", "current_revision", "bigint", "NO")

	// Composite PK (issue_id, revision) is enforced: same pair twice fails.
	runDoltSQL(t, dir, `INSERT INTO issue_versions (issue_id, revision, epoch, change_at) VALUES ('iv-1', 1, 1, '2026-09-01 00:00:00')`)
	if err := runDoltSQLExpectingError(t, dir, `INSERT INTO issue_versions (issue_id, revision, epoch, change_at) VALUES ('iv-1', 1, 1, '2026-09-01 00:00:01')`); err == nil {
		t.Error("duplicate (issue_id, revision) insert into issue_versions succeeded, want primary key violation")
	}

	// Singleton PK (id) is enforced the same way.
	runDoltSQL(t, dir, `INSERT INTO store_epoch (id) VALUES (1)`)
	if err := runDoltSQLExpectingError(t, dir, `INSERT INTO store_epoch (id) VALUES (1)`); err == nil {
		t.Error("duplicate id=1 insert into store_epoch succeeded, want primary key violation")
	}
	if err := runDoltSQLExpectingError(t, dir, `INSERT INTO store_epoch (id) VALUES (2)`); err == nil {
		t.Error("id=2 insert into store_epoch succeeded, want the singleton CHECK constraint to reject any id other than 1")
	}

	// Compat direction 1: an old (pre-Phase-1) binary only ever issues
	// explicit-column statements against `issues` that don't mention
	// current_revision. Those must still work unchanged post-migration.
	runDoltSQL(t, dir, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES ('old-style-1', 'Old-style insert', 'd', 'des', 'ac', 'n')`)
	rows := queryDoltCSV(t, dir, `SELECT title FROM issues WHERE id = 'old-style-1'`)
	if len(rows) != 1 || rows[0]["title"] != "Old-style insert" {
		t.Fatalf("old-shaped explicit-column insert/select round-trip failed post-migration: %v", rows)
	}
}

// requireDoltDataType is requireDoltColumnShape's data_type-based sibling:
// it asserts the coarse, display-width-independent information_schema
// column (always "bigint"/"int"/"json", never e.g. "bigint(20)"), for
// column types this package has no existing column_type-string precedent
// to match against.
func requireDoltDataType(t *testing.T, dir, tableName, columnName, wantDataType, wantNullable string) {
	t.Helper()
	rows := queryDoltCSV(t, dir, fmt.Sprintf(`
SELECT data_type AS data_type, is_nullable AS is_nullable
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = %s
  AND column_name = %s`, doltSQLString(tableName), doltSQLString(columnName)))
	if len(rows) != 1 {
		t.Fatalf("%s.%s column query returned %d rows, want 1: %v", tableName, columnName, len(rows), rows)
	}
	if got := rows[0]["data_type"]; got != wantDataType {
		t.Fatalf("%s.%s DATA_TYPE = %s, want %s", tableName, columnName, got, wantDataType)
	}
	if got := rows[0]["is_nullable"]; got != wantNullable {
		t.Fatalf("%s.%s IS_NULLABLE = %s, want %s", tableName, columnName, got, wantNullable)
	}
}
