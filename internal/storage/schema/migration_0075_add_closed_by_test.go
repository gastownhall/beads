package schema

import (
	"os"
	"strings"
	"testing"
)

// Migration 0075 (+ ignored/0032) adds closed_by, the actor whose write moved
// the row into closed (types.Issue.ClosedBy, GH#8617).

const (
	migration0075Up        = "0075_add_closed_by.up.sql"
	migration0075Down      = "0075_add_closed_by.down.sql"
	ignoredMigration0032Up = "0032_add_wisps_closed_by.up.sql"
)

// TestLatestVersionIncludesMigration0075 pins the main slot this migration
// claims, 0075, and its ignored twin's, 0032, superseding 0074's main pin and
// ignored 0031's. A hardcoded literal for the reason the earlier ones were: a
// drift for the wrong reason is caught by the shape test below failing to
// explain why 75 is closed_by-shaped.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want, wantIgnored = 75, 32
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (closed_by)", got, want)
	}
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (wisps.closed_by)", got, wantIgnored)
	}
}

// TestMigration0075AddsClosedByToBothPlanes checks the frozen bytes, the CLI
// bundle substitute and the ignored twin directly, no dolt binary required.
// The engine-level behavior (column present, existing rows untouched, replay a
// no-op) is pinned by the embedded store's 0075 test.
func TestMigration0075AddsClosedByToBothPlanes(t *testing.T) {
	const columnDDL = "ADD COLUMN closed_by VARCHAR(255) DEFAULT "
	upSQL, err := MigrationSQL(migration0075Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0075Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE issues " + columnDDL + "'''''",
		"'ALTER TABLE wisps " + columnDDL + "'''''",
		"COLUMN_NAME = 'closed_by'",
		// The wisps arm is guarded on the TABLE existing, not only on the
		// column: older workspaces created issues-only (0054 precedent).
		"INFORMATION_SCHEMA.TABLES",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0075 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0075 must keep its guarded PREPARE blocks: they make a raw .up.sql replay onto an already-migrated store a no-op")
	}
	// No backfill: the column add must not rewrite any bead row.
	for _, line := range strings.Split(upSQL, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(strings.ToUpper(trimmed), "UPDATE ") {
			t.Errorf("0075 rewrites rows (%s); closed_by has no backfill", trimmed)
		}
	}

	bundle := cliCompatibleMigrationSQL(migration0075Up, upSQL)
	for _, want := range []string{
		"ALTER TABLE issues " + columnDDL + "'';",
		"ALTER TABLE wisps " + columnDDL + "'';",
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
	for _, want := range []string{
		"'ALTER TABLE issues DROP COLUMN closed_by'",
		"'ALTER TABLE wisps DROP COLUMN closed_by'",
	} {
		if !strings.Contains(string(downBytes), want) {
			t.Errorf("0075 down migration missing %q", want)
		}
	}

	ignoredSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) error = %v", ignoredMigration0032Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE wisps " + columnDDL + "'''''",
		"COLUMN_NAME = 'closed_by'",
		"INFORMATION_SCHEMA.TABLES",
	} {
		if !strings.Contains(ignoredSQL, want) {
			t.Errorf("ignored/0032 missing %q\nfull SQL:\n%s", want, ignoredSQL)
		}
	}
	if strings.Contains(ignoredSQL, "ALTER TABLE issues") {
		t.Error("ignored/0032 must not touch issues: it is a replicated table the ignored series never owns")
	}
}
