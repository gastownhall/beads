package schema

import (
	"os"
	"strings"
	"testing"
)

// Migration 0075 (+ ignored/0032) adds closed_by, the durable close-actor
// column (types.Issue.ClosedBy, GH#8617): the close-side twin of created_by,
// stamped from the write's actor by the close-lifecycle writers and cleared
// on reopen.

const (
	migration0075Up        = "0075_add_closed_by.up.sql"
	migration0075Down      = "0075_add_closed_by.down.sql"
	ignoredMigration0032Up = "0032_add_wisps_closed_by.up.sql"
)

// TestLatestVersionIncludesMigration0075 pins the main slot this migration
// claims, 75, superseding TestLatestVersionIncludesMigration0074
// (migration_0074_create_id_tombstones_test.go) now that 0075 claims the next
// free slot -- only one such pin lives at a time, matching how that test
// itself already superseded 0073's own version. Its ignored twin, 0032, is
// likewise the new top of the ignored series: that pin moved here from
// TestLatestIgnoredVersionIncludesMigration0031.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (the closed_by column)", got, want)
	}
}

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot the
// wisps half of closed_by claims, superseding
// TestLatestIgnoredVersionIncludesMigration0031's version pin (its repair
// assertions live on in TestMigration0031RepairRegistered).
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (wisps.closed_by)", got, wantIgnored)
	}
}

// TestMigration0075AddsClosedByToBothPlanes checks the frozen bytes and the
// CLI-bundle substitute directly, no dolt binary required. The shape rules
// it pins are the ones the embedded replay test exercises against a real
// engine.
func TestMigration0075AddsClosedByToBothPlanes(t *testing.T) {
	upSQL, err := MigrationSQL(migration0075Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0075Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE issues ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''",
		"'ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''",
		"COLUMN_NAME = 'closed_by'",
		// The wisps arm is guarded on the TABLE existing, not only on the
		// column: older workspaces created issues-only (0054/0073 precedent).
		"INFORMATION_SCHEMA.TABLES",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0075 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0075 must keep its guarded PREPARE blocks: they make a raw .up.sql replay onto an already-migrated store a no-op")
	}
	// The column matches created_by's shape exactly: VARCHAR(255) with the
	// empty default, so no existing row's cell is rewritten and a row closed
	// before this column reads '' rather than NULL.
	if !strings.Contains(upSQL, "closed_by VARCHAR(255) DEFAULT ''''") {
		t.Error("0075 must declare closed_by VARCHAR(255) DEFAULT '' (the created_by shape)")
	}
	// No backfill, no bead row touched: the actor of a historical close lives
	// only in the clone-local journal, and a migration that wrote it would
	// fork every clone (see the migration's header).
	for _, forbidden := range []string{"UPDATE issues", "UPDATE wisps", "INSERT INTO issues", "INSERT INTO wisps", "DELETE FROM"} {
		if strings.Contains(strings.ToUpper(upSQL), forbidden) {
			t.Errorf("0075 must not write bead rows (%q found): migrations never touch a bead", forbidden)
		}
	}

	bundle := cliCompatibleMigrationSQL(migration0075Up, upSQL)
	for _, want := range []string{
		"ALTER TABLE issues ADD COLUMN closed_by VARCHAR(255) DEFAULT '';",
		"ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT '';",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("0075's CLI bundle substitute missing direct DDL %q: a pre-2.3 CLI no-ops the prepared ALTER", want)
		}
	}
	if !cliSubstituteAssumesWispTables(migration0075Up) {
		t.Error("0075's CLI substitute ALTERs wisps unguarded, so it must be listed in cliSubstituteAssumesWispTables")
	}

	downBytes, err := os.ReadFile("migrations/" + migration0075Down)
	if err != nil {
		t.Fatalf("read %s: %v", migration0075Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"'ALTER TABLE issues DROP COLUMN closed_by'",
		"'ALTER TABLE wisps DROP COLUMN closed_by'",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0075 down migration missing %q\nfull SQL:\n%s", want, downSQL)
		}
	}

	// The ignored twin mirrors the guarded definition exactly, or a fresh
	// clone's wisps table diverges from an upgraded one's.
	ignoredSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) error = %v", ignoredMigration0032Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''",
		"COLUMN_NAME = 'closed_by'",
	} {
		if !strings.Contains(ignoredSQL, want) {
			t.Errorf("ignored/0032 missing %q\nfull SQL:\n%s", want, ignoredSQL)
		}
	}
	// The twin must stay a no-op on an in-place-upgraded workspace, where
	// synced 0075 already added the column: the guard, not the cursor, is
	// what makes it idempotent.
	if !strings.Contains(ignoredSQL, "INFORMATION_SCHEMA.TABLES") {
		t.Error("ignored/0032 must guard on the wisps table existing (0013/0029 precedent)")
	}
}
