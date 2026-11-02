package schema

import (
	"os"
	"strings"
	"testing"
)

const (
	migration0075Up        = "0075_widen_residual_content_columns.up.sql"
	ignoredMigration0031Up = "0031_widen_event_comment_columns.up.sql"
)

// TestLatestVersionIncludesMigration0075 pins the slots this migration claims,
// main 0075 and ignored 0031, superseding 0074's own version of this test.
// The hardcoded literals are for the reason the earlier ones were: a drift to
// 75/31 for the wrong reason is caught by the shape tests below failing to
// explain why 75 is widening-shaped.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (residual content-column widening)", got, want)
	}
	const wantIgnored = 31
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (event comment widening on fresh clones)", got, wantIgnored)
	}
}

// TestMigration0075WidensEventsCommentGuarded is a DB-independent check of
// the frozen bytes: the events.comment widening reaches the file as an
// independently guarded PREPARE block whose MODIFY restates the full column
// definition (nullable, no default), and the bundle route carries the direct
// ALTER instead. The engine-level behavior is pinned by the embedded store's
// 0075 test; the CLI bundle shape by TestMigration0075BundleWidensAllFive-
// ThroughDoltCLI in schema_test.go.
func TestMigration0075WidensEventsCommentGuarded(t *testing.T) {
	upSQL, err := MigrationSQL(migration0075Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) = %v", migration0075Up, err)
	}
	for _, want := range []string{
		"@events_comment_needs_fix",
		"'ALTER TABLE events MODIFY COLUMN comment LONGTEXT'",
		"@wisp_events_comment_needs_fix",
		"'ALTER TABLE wisp_events MODIFY COLUMN comment LONGTEXT DEFAULT '''''",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0075 up migration missing %q", want)
		}
	}
	// A shared guard between events.comment and a sibling column would
	// mishandle uneven drift; every widening must resolve its own variable
	// (a generic @needs_fix, as 0073 spells a shared one, must not appear).
	if strings.Contains(upSQL, "@needs_fix") {
		t.Error("0075 uses a shared guard variable @needs_fix; each column needs its own probe")
	}

	got := cliCompatibleMigrationSQL(migration0075Up, upSQL)
	if got == upSQL {
		t.Fatal("0075 has no registered CLI substitute; the prepared ALTER would silently no-op on a pre-2.3 CLI bundle")
	}
	if !strings.Contains(got, "ALTER TABLE events MODIFY COLUMN comment LONGTEXT;") {
		t.Errorf("0075 CLI substitute missing direct events.comment widening: %s", got)
	}
	if !strings.Contains(got, "ALTER TABLE wisp_events MODIFY COLUMN comment LONGTEXT DEFAULT '';") {
		t.Errorf("0075 CLI substitute missing direct wisp_events.comment widening: %s", got)
	}
	if !cliSubstituteAssumesWispTables(migration0075Up) {
		t.Error("0075 is not registered on cliSubstituteAssumesWispTables; its substitute's bare wisp_events MODIFY aborts replays over never-synced clones")
	}
	for _, forbidden := range []string{"PREPARE", "@events_comment_needs_fix"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("0075 CLI substitute contains prepared-DDL marker %q", forbidden)
		}
	}

	// The ignored-series twin must carry the identical guarded block: events
	// is dolt-ignored since 0062, so a fresh clone materializes the column
	// from ignored/0019 (TEXT) and only ignored/0031 widens it there.
	ignoredSQL, err := IgnoredMigrationSQL(ignoredMigration0031Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0031Up, err)
	}
	for _, want := range []string{
		"@events_comment_needs_fix",
		"'ALTER TABLE events MODIFY COLUMN comment LONGTEXT'",
		"@wisp_events_comment_needs_fix",
		"'ALTER TABLE wisp_events MODIFY COLUMN comment LONGTEXT DEFAULT '''''",
	} {
		if !strings.Contains(ignoredSQL, want) {
			t.Errorf("ignored/0031 missing %q", want)
		}
	}

	downBytes, err := os.ReadFile("migrations/0075_widen_residual_content_columns.down.sql")
	if err != nil {
		t.Fatalf("read 0075 down migration: %v", err)
	}
	if strings.Contains(strings.ToUpper(string(downBytes)), "ALTER TABLE") {
		t.Error("0075 down migration must stay a comment-only no-op: narrowing would silently truncate stored values")
	}
}
