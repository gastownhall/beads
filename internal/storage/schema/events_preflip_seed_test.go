package schema

import (
	"strings"
	"testing"
)

const ignoredMigration0031Up = "0031_restore_preflip_events.up.sql"

// TestLatestIgnoredVersionIncludesMigration0031 pins the ignored slot the
// pre-upgrade events restore claims (gastownhall/beads#7039), directly above
// wisp_id_tombstones' (ignored 0030), and that the slot carries its repair.
func TestLatestIgnoredVersionIncludesMigration0031(t *testing.T) {
	const wantIgnored = 31
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (pre-upgrade events restore)", got, wantIgnored)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, wantIgnored}]; !ok {
		t.Fatalf("ignored 0031 has no pre-migration repair registered; the marker alone restores nothing")
	}
}

// TestEventsFlipDropCommitMessageMatches0062 pins the message the restore
// locates its sources by against the frozen 0062 bytes that write it.
func TestEventsFlipDropCommitMessageMatches0062(t *testing.T) {
	flipSQL, err := MigrationSQL("0062_events_dolt_ignore.up.sql")
	if err != nil {
		t.Fatalf("MigrationSQL(0062) = %v", err)
	}
	want := "CALL DOLT_COMMIT('--skip-empty', '-m', '" + eventsFlipDropCommitMessage + "');"
	if !strings.Contains(flipSQL, want) {
		t.Fatalf("0062 no longer commits the drop with %q; the restore would find no source", eventsFlipDropCommitMessage)
	}
}

// TestMigration0031MarkerIsSeenTouchingEvents pins the raw-text declaration
// the pending-migration dirty-table guard relies on: the marker's repair
// writes events, so a tracked, dirty events table must refuse the pass.
func TestMigration0031MarkerIsSeenTouchingEvents(t *testing.T) {
	markerSQL, err := IgnoredMigrationSQL(ignoredMigration0031Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0031Up, err)
	}
	if !migrationSQLTouchesTable(markerSQL, "events") {
		t.Fatal("ignored 0031 does not declare its events write; the dirty-table guard would not see it")
	}
	if migrationSQLTouchesTable(markerSQL, "issues") || migrationSQLTouchesTable(markerSQL, "bd_events_journal") {
		t.Fatal("ignored 0031 declares a write to a table its repair never touches")
	}
}
