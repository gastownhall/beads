package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

const (
	migration0075Up        = "0075_widen_residual_content_columns.up.sql"
	migration0075Down      = "0075_widen_residual_content_columns.down.sql"
	ignoredMigration0031Up = "0031_widen_event_comment_columns.up.sql"
)

// TestLatestVersionIncludesMigration0075 pins the slots this migration claims,
// main 0075 and ignored 0031, superseding 0074's own version of this test.
// A hardcoded literal for the reason the earlier ones were: a drift to 75 for
// the wrong reason is caught by the column tests below failing to explain why
// 75 widens the residual content columns.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (residual content column widening)", got, want)
	}
	const wantIgnored = 31
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (event comment widening on fresh clones)", got, wantIgnored)
	}
}

// TestMigration0075GuardsEachColumnOnItsOwn pins the frozen text's shape: one
// COLUMN_TYPE probe and one MODIFY per widened column, never a shared guard
// (the #4353 re-MODIFY hazard 0057 records), and a down migration that runs
// no DDL, since narrowing would cut values written after the widening.
func TestMigration0075GuardsEachColumnOnItsOwn(t *testing.T) {
	upSQL, err := MigrationSQL(migration0075Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v", migration0075Up, err)
	}
	for _, c := range []struct{ guard, probe, alter string }{
		{"@events_comment_needs_widen", "TABLE_NAME = 'events'", "'ALTER TABLE events MODIFY COLUMN comment LONGTEXT'"},
		{"@wisp_events_comment_needs_widen", "TABLE_NAME = 'wisp_events'", "'ALTER TABLE wisp_events MODIFY COLUMN comment LONGTEXT DEFAULT '''''"},
		{"@issue_snapshots_original_content_needs_widen", "COLUMN_NAME = 'original_content'", "'ALTER TABLE issue_snapshots MODIFY COLUMN original_content LONGTEXT NOT NULL'"},
		{"@issue_snapshots_archived_events_needs_widen", "COLUMN_NAME = 'archived_events'", "'ALTER TABLE issue_snapshots MODIFY COLUMN archived_events LONGTEXT'"},
		{"@compaction_snapshot_json_needs_widen", "COLUMN_NAME = 'snapshot_json'", "'ALTER TABLE compaction_snapshots MODIFY COLUMN snapshot_json LONGBLOB NOT NULL'"},
	} {
		set := strings.Index(upSQL, "SET "+c.guard+" = (")
		use := strings.Index(upSQL, "IF("+c.guard+" = 1,")
		if set < 0 || use < set {
			t.Errorf("0075 has no guard %s set and then tested", c.guard)
			continue
		}
		if !strings.Contains(upSQL[set:use], c.probe) {
			t.Errorf("guard %s does not probe %s", c.guard, c.probe)
		}
		if got := strings.Count(upSQL, c.alter); got != 1 {
			t.Errorf("0075 has %d copies of %s, want exactly 1", got, c.alter)
		}
		if !strings.HasPrefix(strings.TrimSpace(upSQL[use+len("IF("+c.guard+" = 1,"):]), c.alter) {
			t.Errorf("guard %s does not select its own %s", c.guard, c.alter)
		}
	}
	if got := strings.Count(upSQL, "PREPARE stmt FROM @sql"); got != 5 {
		t.Errorf("0075 has %d PREPARE blocks, want one per column (5)", got)
	}

	downBytes, err := os.ReadFile("migrations/" + migration0075Down)
	if err != nil {
		t.Fatalf("read %s: %v", migration0075Down, err)
	}
	for _, line := range strings.Split(string(downBytes), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "--") {
			t.Errorf("0075 down migration runs %q; it must stay a comment-only no-op", line)
		}
	}
}

// TestMigration0075BundleWidensAllFiveThroughDoltCLI runs the whole CLI bundle
// on a fresh dolt repository and reads back the five column definitions. The
// dolt CLI skips a PREPAREd ALTER without an error, so only the registered
// direct-DDL substitute can widen them here, and a zero exit status proves
// nothing on its own.
func TestMigration0075BundleWidensAllFiveThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "widen-residual-content")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	rows := queryDoltCSV(t, dir, `
SELECT CONCAT(TABLE_NAME, '.', COLUMN_NAME) AS col, COLUMN_TYPE AS type, IS_NULLABLE AS nullable
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND CONCAT(TABLE_NAME, '.', COLUMN_NAME) IN ('events.comment', 'wisp_events.comment',
      'issue_snapshots.original_content', 'issue_snapshots.archived_events', 'compaction_snapshots.snapshot_json')`)
	got := map[string]string{}
	for _, row := range rows {
		got[row["col"]] = row["type"] + " " + row["nullable"]
	}
	for col, want := range map[string]string{
		"events.comment":                     "longtext YES",
		"wisp_events.comment":                "longtext YES",
		"issue_snapshots.original_content":   "longtext NO",
		"issue_snapshots.archived_events":    "longtext YES",
		"compaction_snapshots.snapshot_json": "longblob NO",
	} {
		if got[col] != want {
			t.Errorf("CLI bundle %s = %q, want %q", col, got[col], want)
		}
	}
}

// TestMigration0075NoopsWithoutWispEventsThroughDoltCLI replays the frozen
// 0075 and ignored/0031 texts through the dolt CLI on a database without the
// two clone-local event tables: their NULL probes take the SELECT 1 branch,
// so the replay succeeds and creates nothing. It is the CLI's missing-table
// compatibility control only; the embedded engine tests prove the prepared
// ALTERs widen.
func TestMigration0075NoopsWithoutWispEventsThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "widen-residual-content-no-events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create no-events dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	seedSQL := fmt.Sprintf(`
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS wisp_events;
DELETE FROM schema_migrations WHERE version = %d;
`, LatestVersion())
	migrationSQL, err := mainSource.files.ReadFile("migrations/" + migration0075Up)
	if err != nil {
		t.Fatalf("read 0075 migration: %v", err)
	}
	ignoredSQL, err := IgnoredMigrationSQL(ignoredMigration0031Up)
	if err != nil {
		t.Fatalf("read ignored 0031 migration: %v", err)
	}
	runDoltSQL(t, dir, seedSQL+"\n"+string(migrationSQL)+"\n"+ignoredSQL)

	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('events', 'wisp_events')`, "0")

	// Control: the registered substitute ALTERs both tables unguarded, so the
	// same absent tables make it fail; without this, "the frozen text
	// tolerates it" would be indistinguishable from "nothing here can fail".
	substitute := cliCompatibleMigrationSQL(migration0075Up, string(migrationSQL))
	if substitute == string(migrationSQL) {
		t.Fatal("0075 has no registered CLI substitute; this test's control is vacuous")
	}
	if err := runDoltSQLExpectingError(t, dir, substitute); err == nil {
		t.Fatal("0075 CLI substitute unexpectedly succeeded against absent events and wisp_events")
	}
}
