package schema

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const ignoredMigration0032Up = "0032_restore_wisp_is_blocked.up.sql"

// TestIgnored32IsLatest pins the ignored slot the
// wisps.is_blocked restore claims (gastownhall/beads#4468, #5257), directly
// above the pre-upgrade events restore's (ignored 0031), and that the slot
// dispatches to ensureWispIsBlockedForRecompute: the marker alone restores
// nothing.
func TestIgnored32IsLatest(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (wisps.is_blocked restore)", got, wantIgnored)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, wantIgnored}]; !ok {
		t.Fatalf("ignored 0032 has no pre-migration repair registered; the marker alone restores nothing")
	}
}

// TestIgnored32MarkerIsInert pins that the marker's only statement
// is SELECT 1, so the work stays in the probed Go repair and a replay of the
// file can never issue an unguarded ALTER.
func TestIgnored32MarkerIsInert(t *testing.T) {
	body, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0032Up, err)
	}
	var stmts []string
	for _, line := range strings.Split(body, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			stmts = append(stmts, l)
		}
	}
	if len(stmts) != 1 || stmts[0] != "SELECT 1;" {
		t.Fatalf("ignored 0032 statements = %q, want only SELECT 1;", stmts)
	}
}

// TestIgnored32RestoresColumn drives the real
// runMigrations over the one pending file a clone at ignored cursor 31 has:
// wisps exists without is_blocked, nothing below the cursor is pending, and
// the {ignored_schema_migrations, 32} repair must restore the column and its
// index before the marker's cursor row is recorded.
func TestIgnored32RestoresColumn(t *testing.T) {
	orig := stderr
	stderr = &bytes.Buffer{}
	defer func() { stderr = orig }()

	db := newWispIsBlockedDriftDB(t, ignoredMigration0032Up, 32)
	if _, err := runMigrations(context.Background(), db, ignoredSource, 31, 32, false); err != nil {
		t.Fatalf("runMigrations(ignoredSource, v32) = %v, want nil", err)
	}
	if err := db.expectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if !db.hasIsBlockedColumn {
		t.Error("ignored/0032 was recorded without restoring wisps.is_blocked")
	}
	if !db.hasIsBlockedIndex {
		t.Error("ignored/0032 was recorded without restoring idx_wisps_is_blocked")
	}
	if !db.cursorInserted {
		t.Error("ignored/0032's cursor row (version 32) was never recorded")
	}
}
