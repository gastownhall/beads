package schema

import (
	"strings"
	"testing"
)

const ignoredMigration0032Up = "0032_wisp_is_blocked_restore_marker.up.sql"

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot the
// wisps.is_blocked restore claims (gastownhall/beads#4468, #5257), directly
// above the pre-upgrade events restore marker (ignored 0031); that the slot
// is unique and the file above it is this marker alone; and that the slot
// carries its repair.
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (wisps.is_blocked restore marker)", got, wantIgnored)
	}
	atSlot := 0
	for _, mf := range ignoredSource.list() {
		if mf.version == wantIgnored {
			atSlot++
			if mf.name != ignoredMigration0032Up {
				t.Fatalf("ignored slot 32 is held by %s, want %s", mf.name, ignoredMigration0032Up)
			}
		}
	}
	if atSlot != 1 {
		t.Fatalf("ignored 32 has %d files at its slot, want exactly 1", atSlot)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, wantIgnored}]; !ok {
		t.Fatalf("ignored 0032 has no pre-migration repair registered; the marker alone restores nothing")
	}
}

// TestIgnored32MarkerIsInert pins that the marker's body changes nothing:
// every statement outside its comments must be the inert SELECT 1. The
// restore work lives in restoreWispIsBlocked; a body that wrote DDL or DML
// would replay on every clone, not only drifted ones.
func TestIgnored32MarkerIsInert(t *testing.T) {
	markerSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0032Up, err)
	}
	var body strings.Builder
	for _, line := range strings.Split(markerSQL, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		body.WriteString(trimmed)
		body.WriteString("\n")
	}
	if got, want := body.String(), "SELECT 1;\n"; got != want {
		t.Fatalf("ignored 0032 body = %q, want %q: the marker must change nothing itself", got, want)
	}
}

// TestIgnored32MarkerDeclaresWispsOnly pins the raw-text declaration the
// pending-migration dirty-table guard relies on: the marker's repair writes
// wisps, so a tracked, dirty wisps table must refuse the pass, while the
// tables the repair only reads must stay undeclared.
func TestIgnored32MarkerDeclaresWispsOnly(t *testing.T) {
	markerSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0032Up, err)
	}
	if !migrationSQLTouchesTable(markerSQL, "wisps") {
		t.Fatal("ignored 0032 does not declare its wisps writes; the dirty-table guard would not see them")
	}
	for _, table := range []string{"issues", "dependencies", "wisp_dependencies", "events", "bd_events_journal"} {
		if migrationSQLTouchesTable(markerSQL, table) {
			t.Fatalf("ignored 0032 declares a write to %s; the repair reads it but never rewrites it", table)
		}
	}
}
