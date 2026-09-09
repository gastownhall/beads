package schema

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// Phase 2 of the versioned-beads epic (be-hs42e / gastownhall/beads#6132,
// this slice: be-hs42e.3 / #6135) adds one column: issue_versions gains a
// NOT NULL attribution_status (design §16.4, R14), written by
// RecordVersionInTx / attributionStatusForActor
// (internal/storage/issueops/version_history.go) from the same build that
// ships this migration, so the NOT NULL constraint never rejects a
// pre-existing row. Steps 1-5 of design §16.3 (the version_id PK swap and
// participation_generation) are a different bead's scope and are not in this
// migration file.
//
// Step 7 (added at review, donnabox on #6358 item 4) retypes
// issue_versions.durable_state from JSON to LONGBLOB: Dolt's JSON type
// renormalizes numbers, so it cannot keep the verbatim bytes (R5.1) that a
// content-derived token hashes. The writer now stores the RFC 8785 (JCS)
// canonical bytes; the table is empty in this era, so no data converts.

const migration0068Up = "0068_add_attribution_status.up.sql"
const migration0068Down = "0068_add_attribution_status.down.sql"

// TestLatestVersionIncludesMigration0068 (pinning LatestVersion() == 68) is
// superseded by TestLatestVersionIncludesMigration0069
// (migration_0069_add_removed_restriction_test.go) now that 0069 claims the
// next free slot — only one such pin lives at a time, matching how this
// test itself already superseded 0067's own version.

// TestMigration0068AddsAttributionStatus is a pure-Go, DB-independent check
// of the frozen migration bytes themselves — it runs even where no `dolt`
// binary is available.
//
// It pins the same guarded-PREPARE shape 0067 uses and for the same reason:
// the guard is what makes a raw replay of this file onto an already-migrated
// store a no-op (internal/storage/dolt's pr4107 replay harness requires this
// of every migration >= 0046), and Dolt accepts no unprepared conditional
// ADD COLUMN. The cost is the same pre-2.3 CLI hazard 0060/0065/0066/0067
// carry, so this migration needs the same direct-DDL override in
// cliCompatibleMigrationSQL, policed by the same two guard tests
// (TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified and
// TestAllMigrationsSQLUsesDirectDDLForKnownCLIIncompatibilities).
func TestMigration0068AddsAttributionStatus(t *testing.T) {
	upSQL, err := MigrationSQL(migration0068Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0068Up, err)
	}
	for _, want := range []string{
		"ALTER TABLE issue_versions ADD COLUMN attribution_status VARCHAR(20) NOT NULL",
		"COLUMN_NAME = 'attribution_status'",
		"@issue_versions_as_needs_add",
		// Step 7: the durable_state retype, guarded on DATA_TYPE (0057's
		// shape) so a replay onto a store already at LONGBLOB no-ops.
		"ALTER TABLE issue_versions MODIFY COLUMN durable_state LONGBLOB",
		"COLUMN_NAME = 'durable_state'",
		"@issue_versions_ds_needs_retype",
		"DATA_TYPE <> 'longblob'",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0068 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0068 up migration must keep its guarded PREPARE block — it is what makes a raw .up.sql replay onto an already-migrated store a no-op, and Dolt accepts no unprepared conditional ADD COLUMN. Unwrapping it also invalidates cliMigration0068AddAttributionStatus.")
	}
	// The bundle override is what keeps the PREPARE above off the pre-2.3
	// CLI path. Assert it directly rather than trusting the two schema_test
	// assertions to stay pointed at this migration.
	for _, want := range []string{
		"ALTER TABLE issue_versions ADD COLUMN attribution_status VARCHAR(20) NOT NULL;",
		// Step 7's retype has to reach the bundle as direct DDL too, or a
		// fresh CLI-built database keeps 0067's JSON column while the
		// runtime has LONGBLOB.
		"ALTER TABLE issue_versions MODIFY COLUMN durable_state LONGBLOB;",
	} {
		if !strings.Contains(cliCompatibleMigrationSQL(migration0068Up, upSQL), want) {
			t.Errorf("0068's CLI bundle substitute missing direct DDL %q", want)
		}
	}
	if cliSubstituteAssumesWispTables(migration0068Up) {
		t.Error("0068's CLI substitute touches only issue_versions, which has no wisps-side counterpart table — it must not be listed in cliSubstituteAssumesWispTables")
	}

	// down.sql files are not part of the embedded FS (only migrations/*.up.sql
	// is //go:embed'd — see mainSource.files), so unlike the up side above,
	// this reads straight from disk by package-relative path, matching
	// TestMigration0067AddsVersionedBeadsSchema's precedent.
	downBytes, err := os.ReadFile("migrations/" + migration0068Down)
	if err != nil {
		t.Fatalf("read %s: %v, want the migration file to exist", migration0068Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"ALTER TABLE issue_versions DROP COLUMN attribution_status",
		"COLUMN_NAME = 'attribution_status'",
		// Step 7's reverse: back to the JSON type 0067 created, guarded on
		// DATA_TYPE so an already-rolled-back store no-ops.
		"ALTER TABLE issue_versions MODIFY COLUMN durable_state JSON",
		"COLUMN_NAME = 'durable_state'",
		"@issue_versions_ds_is_longblob",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0068 down migration missing %q\nfull SQL:\n%s", want, downSQL)
		}
	}
	// Only migrations/*.up.sql is embedded into the CLI fresh bundle
	// (mainSource.files), so the pre-2.3 prepared-DDL hazard never reaches a
	// down migration and the guard is free — 0060's/0067's downs are the
	// precedent.
	if !strings.Contains(strings.ToUpper(downSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0068 down migration must guard its DROP COLUMN the way the up migration guards its ADD COLUMN, so a partially-applied or already-rolled-back workspace rolls back safely")
	}
}

// TestMigration0068AddsAttributionStatusThroughDoltCLI applies the full
// migration bundle through a real `dolt` binary (skipped without one — see
// testutil.RequireDoltBinary) and checks the shape acceptance criteria a
// pure-Go SQL-text check cannot: actual column type/nullability as Dolt
// reports it, and that the NOT NULL constraint is real (an insert that omits
// attribution_status must fail, matching the fixture fix this migration
// forced onto TestMigration0067AddsVersionedBeadsSchemaThroughDoltCLI).
func TestMigration0068AddsAttributionStatusThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := t.TempDir()
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	requireDoltColumnShape(t, dir, "issue_versions", "attribution_status", "varchar(20)", "NO")
	// Step 7: 0067 created durable_state as JSON; after 0068 it is the
	// byte-preserving LONGBLOB the writer's JCS canonical form needs.
	requireDoltDataType(t, dir, "issue_versions", "durable_state", "longblob", "YES")
	requireDoltNoRows(t, dir, "SELECT issue_id FROM issue_versions", "issue_versions")

	if err := runDoltSQLExpectingError(t, dir, `INSERT INTO issue_versions (issue_id, revision, epoch, change_at) VALUES ('iv-1', 1, 1, '2026-09-01 00:00:00')`); err == nil {
		t.Error("insert into issue_versions omitting attribution_status succeeded, want a NOT NULL violation")
	}
	runDoltSQL(t, dir, `INSERT INTO issue_versions (issue_id, revision, epoch, change_at, attribution_status) VALUES ('iv-1', 1, 1, '2026-09-01 00:00:00', 'claimed')`)
	rows := queryDoltCSV(t, dir, `SELECT attribution_status FROM issue_versions WHERE issue_id = 'iv-1'`)
	if len(rows) != 1 || rows[0]["attribution_status"] != "claimed" {
		t.Fatalf("attribution_status round-trip failed post-migration: %v", rows)
	}

	// Step 7's actual property, measured rather than inferred from the
	// type name: bytes written to durable_state come back byte for byte.
	// These three number forms are the ones Dolt's JSON type renormalizes
	// (on 2.2.3 the same INSERT into a JSON column reads back as
	// {"a":1,"big":9007199254740992,"e":1e+300}), so a regression to JSON
	// fails here even if the DATA_TYPE assertion above were loosened.
	const verbatim = `{"a":1.0,"big":9007199254740993,"e":1e300}`
	runDoltSQL(t, dir, `INSERT INTO issue_versions (issue_id, revision, epoch, change_at, attribution_status, durable_state) VALUES ('iv-2', 1, 1, '2026-09-01 00:00:00', 'claimed', '`+verbatim+`')`)
	rows = queryDoltCSV(t, dir, `SELECT durable_state FROM issue_versions WHERE issue_id = 'iv-2'`)
	if len(rows) != 1 || rows[0]["durable_state"] != verbatim {
		t.Fatalf("durable_state round-trip changed the bytes post-migration: got %v, want %q", rows, verbatim)
	}
}
