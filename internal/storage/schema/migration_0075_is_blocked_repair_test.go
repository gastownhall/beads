package schema

import (
	"strings"
	"testing"
)

const (
	migration0075Up        = "0075_recompute_mixed_is_blocked_dolt11886.up.sql"
	ignoredMigration0032Up = "0032_recompute_mixed_is_blocked_dolt11886.up.sql"
)

// TestLatestVersionIncludesMigration0075 pins the main slot this migration
// claims, 0075, superseding 0074's own version of this test (see
// migration_0074_create_id_tombstones_test.go). A hardcoded literal for the
// reason the earlier ones were: a drift to 75 for the wrong reason is caught
// by the marker-shape test below failing to explain why 75 is an obligation
// marker, not DDL.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (is_blocked repair obligation marker)", got, want)
	}
}

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot this
// repair's clone-local half claims, 0032, superseding 0031's latest-ignored
// pin (its repair registration stays pinned in its own file), and that both
// the events restore (0031) and this repair (0032) carry their hooks: a
// marker alone repairs nothing.
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (is_blocked repair clone-local marker)", got, wantIgnored)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, wantIgnored}]; !ok {
		t.Fatalf("ignored 0032 has no pre-migration repair registered; the marker alone repairs nothing")
	}
}

// TestMigration0075MarkerIsSeenTouchingIssuesAndMetadata pins the raw-text
// declaration the pending-migration dirty-table guard relies on: the marker's
// repair writes metadata (the obligation key) and issues (the recomputed
// flags), so a store with uncommitted edits to either must refuse the pass
// typed, before any repair write. The body itself is inert.
func TestMigration0075MarkerIsSeenTouchingIssuesAndMetadata(t *testing.T) {
	upSQL, err := MigrationSQL(migration0075Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) = %v", migration0075Up, err)
	}
	for _, table := range []string{"metadata", "issues"} {
		if !migrationSQLTouchesTable(upSQL, table) {
			t.Errorf("pending-migration guard does not see %s in %s; uncommitted edits to it would ride the repair's commits", table, migration0075Up)
		}
	}
	if !strings.Contains(upSQL, "SELECT 1;") {
		t.Errorf("%s body is not the inert marker; frozen bytes must stay SELECT 1", migration0075Up)
	}
}

// TestMigration0032MarkerTouchesNoDirtyGuardTable pins the inverse property
// for the clone-local half: its header deliberately names no table next to a
// write verb, because the ignored-series dirty-table guard fires MID-PASS
// against the working set a clone already holds, and clone-local planes
// routinely carry permanent working-set rows. A table named here would brick
// those clones' migration pass; the tracked tables this repair publishes are
// protected instead by the pass-level typed guard in migrateUpAfterReconcile
// and the hook's own entry check.
func TestMigration0032MarkerTouchesNoDirtyGuardTable(t *testing.T) {
	upSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0032Up, err)
	}
	for _, table := range []string{"metadata", "issues", "dependencies", "wisps", "events", "wisp_dependencies"} {
		if migrationSQLTouchesTable(upSQL, table) {
			t.Errorf("pending-ignored guard sees %s in %s; a clone holding uncommitted %s rows would abort its pass at this marker", table, ignoredMigration0032Up, table)
		}
	}
	if !strings.Contains(upSQL, "SELECT 1;") {
		t.Errorf("%s body is not the inert marker; frozen bytes must stay SELECT 1", ignoredMigration0032Up)
	}
}
