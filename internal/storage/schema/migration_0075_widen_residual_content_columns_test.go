package schema

import (
	"os"
	"strings"
	"testing"
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
