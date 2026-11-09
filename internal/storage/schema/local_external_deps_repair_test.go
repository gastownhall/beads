package schema

import "testing"

const ignoredMigration0032Up = "0032_type_local_external_dependency_targets.up.sql"

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot the
// local external dependency typing claims (gastownhall/beads#8299), directly
// above the pre-upgrade events restore's (ignored 0031), and that the slot
// carries its repair: the marker alone types nothing.
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (local external dependency typing)", got, wantIgnored)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, wantIgnored}]; !ok {
		t.Fatalf("ignored 0032 has no pre-migration repair registered; the marker alone types nothing")
	}
}

// TestMigration0032MarkerIsSeenTouchingItsTables pins the raw-text declaration
// the pending-migration dirty-table guard relies on: the marker's repair
// writes both dependency tables and both issue tables' is_blocked, so a
// dirty one must refuse the pass.
func TestMigration0032MarkerIsSeenTouchingItsTables(t *testing.T) {
	markerSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0032Up, err)
	}
	for _, table := range []string{"dependencies", "wisp_dependencies", "issues", "wisps"} {
		if !migrationSQLTouchesTable(markerSQL, table) {
			t.Errorf("ignored 0032 does not declare its %s write; the dirty-table guard would not see it", table)
		}
	}
	if migrationSQLTouchesTable(markerSQL, "events") || migrationSQLTouchesTable(markerSQL, "bd_events_journal") {
		t.Error("ignored 0032 declares a write to a table its repair never touches")
	}
}
