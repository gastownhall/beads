package schema

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// R7.1 as-of read (gastownhall/beads#5898 revision 9, gastownhall/beads#6136,
// this slice: be-x5jqd.5) adds one nullable column: issue_versions gains
// removed_restriction VARCHAR(30). issue_versions already carries removed_at
// and removed_reason (migration 0067), virgin and unused until this slice;
// AsOfReadInTx (internal/storage/issueops/asof_read.go) reads all three as
// the durable "this version row was removed" marker. removed_restriction
// carries the categorical restriction removed_at's presence alone cannot:
// gone-retention / gone-erasure / gone-reorganization / unknown, never
// live -- live is the absence of a value, not a stored one.

const migration0069Up = "0069_add_removed_restriction.up.sql"
const migration0069Down = "0069_add_removed_restriction.down.sql"

// TestLatestVersionIncludesMigration0069 pins the real next free slot this
// phase claims, superseding 0068's own version of this test. Deliberately a
// hardcoded literal for the same reason 0067's and 0068's were: LatestVersion()
// drifting to 69 for the wrong reason should still be caught by this test
// failing to explain why 69 is removed-restriction-shaped, which the CLI test
// below checks.
func TestLatestVersionIncludesMigration0069(t *testing.T) {
	const want = 69
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (issue_versions.removed_restriction migration slot claimed by be-x5jqd.5)", got, want)
	}
}

// TestMigration0069AddsRemovedRestriction is a pure-Go, DB-independent check
// of the frozen migration bytes themselves — it runs even where no `dolt`
// binary is available.
//
// It pins the same guarded-PREPARE shape 0067/0068 use and for the same
// reason: the guard is what makes a raw replay of this file onto an
// already-migrated store a no-op, and Dolt accepts no unprepared conditional
// ADD COLUMN. The cost is the same pre-2.3 CLI hazard those migrations carry,
// so this migration needs the same direct-DDL override in
// cliCompatibleMigrationSQL, policed by the same two guard tests
// (TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified and
// TestAllMigrationsSQLUsesDirectDDLForKnownCLIIncompatibilities).
func TestMigration0069AddsRemovedRestriction(t *testing.T) {
	upSQL, err := MigrationSQL(migration0069Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0069Up, err)
	}
	for _, want := range []string{
		"ALTER TABLE issue_versions ADD COLUMN removed_restriction VARCHAR(30)",
		"COLUMN_NAME = 'removed_restriction'",
		"@issue_versions_rr_needs_add",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0069 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0069 up migration must keep its guarded PREPARE block — it is what makes a raw .up.sql replay onto an already-migrated store a no-op, and Dolt accepts no unprepared conditional ADD COLUMN. Unwrapping it also invalidates cliMigration0069AddRemovedRestriction.")
	}
	// The bundle override is what keeps the PREPARE above off the pre-2.3
	// CLI path. Assert it directly rather than trusting the two schema_test
	// assertions to stay pointed at this migration.
	const wantDirectDDL = "ALTER TABLE issue_versions ADD COLUMN removed_restriction VARCHAR(30);"
	if !strings.Contains(cliCompatibleMigrationSQL(migration0069Up, upSQL), wantDirectDDL) {
		t.Errorf("0069's CLI bundle substitute missing direct DDL %q", wantDirectDDL)
	}
	if cliSubstituteAssumesWispTables(migration0069Up) {
		t.Error("0069's CLI substitute touches only issue_versions, which has no wisps-side counterpart table — it must not be listed in cliSubstituteAssumesWispTables")
	}

	// down.sql files are not part of the embedded FS (only migrations/*.up.sql
	// is //go:embed'd — see mainSource.files), so unlike the up side above,
	// this reads straight from disk by package-relative path, matching
	// TestMigration0067AddsVersionedBeadsSchema's / TestMigration0068AddsAttributionStatus's
	// precedent.
	downBytes, err := os.ReadFile("migrations/" + migration0069Down)
	if err != nil {
		t.Fatalf("read %s: %v, want the migration file to exist", migration0069Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"ALTER TABLE issue_versions DROP COLUMN removed_restriction",
		"COLUMN_NAME = 'removed_restriction'",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0069 down migration missing %q\nfull SQL:\n%s", want, downSQL)
		}
	}
	// Only migrations/*.up.sql is embedded into the CLI fresh bundle
	// (mainSource.files), so the pre-2.3 prepared-DDL hazard never reaches a
	// down migration and the guard is free — 0060's/0067's/0068's downs are
	// the precedent.
	if !strings.Contains(strings.ToUpper(downSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0069 down migration must guard its DROP COLUMN the way the up migration guards its ADD COLUMN, so a partially-applied or already-rolled-back workspace rolls back safely")
	}
}

// TestMigration0069AddsRemovedRestrictionThroughDoltCLI applies the full
// migration bundle through a real `dolt` binary (skipped without one — see
// testutil.RequireDoltBinary) and checks the shape acceptance criteria a
// pure-Go SQL-text check cannot: actual column type/nullability as Dolt
// reports it, and that the column stays NULL for a row that never sets it
// (Live is the absence of a value) while round-tripping any of the four
// restriction strings a row does set it to.
func TestMigration0069AddsRemovedRestrictionThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := t.TempDir()
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	requireDoltColumnShape(t, dir, "issue_versions", "removed_restriction", "varchar(30)", "YES")
	requireDoltNoRows(t, dir, "SELECT issue_id FROM issue_versions", "issue_versions")

	runDoltSQL(t, dir, `INSERT INTO issue_versions (issue_id, revision, epoch, change_at, attribution_status) VALUES ('iv-1', 1, 1, '2026-09-01 00:00:00', 'claimed')`)
	// issue_id rides along as a non-NULL anchor column: queryDoltCSV trims the
	// whole `dolt sql -r csv` output before parsing, and a row whose ONLY
	// selected column is NULL renders as a bare blank line, which
	// strings.TrimSpace swallows when it's the trailing line -- silently
	// turning "one row, NULL value" into "zero rows" and passing the wrong
	// assertion for the wrong reason. Selecting issue_id alongside keeps the
	// row's CSV line non-blank so it survives the trim.
	rows := queryDoltCSV(t, dir, `SELECT issue_id, removed_restriction FROM issue_versions WHERE issue_id = 'iv-1'`)
	if len(rows) != 1 || rows[0]["removed_restriction"] != "" {
		t.Fatalf("removed_restriction for a row that never set it = %v, want NULL/empty (Live is the absence of a value)", rows)
	}

	runDoltSQL(t, dir, `INSERT INTO issue_versions (issue_id, revision, epoch, change_at, attribution_status, removed_at, removed_reason, removed_restriction) VALUES ('iv-2', 1, 1, '2026-09-01 00:00:00', 'claimed', '2026-09-02 00:00:00', 'retention window elapsed', 'gone_retention')`)
	rows = queryDoltCSV(t, dir, `SELECT issue_id, removed_restriction FROM issue_versions WHERE issue_id = 'iv-2'`)
	if len(rows) != 1 || rows[0]["removed_restriction"] != "gone_retention" {
		t.Fatalf("removed_restriction round-trip failed post-migration: %v", rows)
	}
}
