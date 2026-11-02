//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigration0075WidensResidualContentColumns pins migration 0075
// (GH#3925) through the real embedded engine, where PREPARE/EXECUTE behaves
// as production expects: a v74 database with rows in all four affected tables
// upgrades to 75 with every widened column at its specified type, nullability
// and default, every pre-existing value (NULLs included) retained, oversized
// writes refused before the migration and byte-identical after it, and a
// replay of both frozen texts a clean no-op. Mixed-width drift on the
// snapshot pair converges, and an absent events/wisp_events or snapshot table
// takes the guard's no-op branch without disturbing the other widenings.
func TestEmbeddedMigration0075WidensResidualContentColumns(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	migrationSQL, err := schema.MigrationSQL("0075_widen_residual_content_columns.up.sql")
	if err != nil {
		t.Fatalf("read 0075 migration: %v", err)
	}
	ignoredSQL, err := schema.IgnoredMigrationSQL("0031_widen_event_comment_columns.up.sql")
	if err != nil {
		t.Fatalf("read ignored/0031 migration: %v", err)
	}

	t.Run("upgrade from 74 retains rows and widens all five", func(t *testing.T) {
		dataDir := seedMainSchemaAt(t, ctx, 74)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		seed := `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('m75-issue', 'seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral)
VALUES ('m75-wisp', 'seed wisp', '', '', '', '', 'open', 2, 'task', 1);
INSERT INTO events (id, issue_id, event_type, actor, comment, created_at)
VALUES ('m75-evt-null', 'm75-issue', 'updated', 'seed', NULL, '2026-10-01 00:00:00');
INSERT INTO events (id, issue_id, event_type, actor, comment, created_at)
VALUES ('m75-evt-small', 'm75-issue', 'commented', 'seed', 'kept small comment', '2026-10-01 00:00:01');
INSERT INTO wisp_events (id, issue_id, event_type, actor, comment, created_at)
VALUES ('m75-wevt', 'm75-wisp', 'commented', 'seed', 'kept wisp comment', '2026-10-01 00:00:02');
INSERT INTO issue_snapshots (id, issue_id, snapshot_time, compaction_level, original_size, compressed_size, original_content, archived_events)
VALUES ('m75-snap', 'm75-issue', '2026-10-01 00:00:00', 1, 20, 20, 'kept snapshot body', NULL);
INSERT INTO compaction_snapshots (id, issue_id, compaction_level, snapshot_json)
VALUES ('m75-comp', 'm75-issue', 1, UNHEX('00ff10aa20bb'));
`
		execFrozenGuard(t, ctx, conn, seed)
		mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed v74 rows')")

		// The caps are live at 74: each oversized write is refused, not
		// silently truncated.
		expectTooLarge(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO events (id, issue_id, event_type, actor, comment, created_at) VALUES ('m75-evt-big', 'm75-issue', 'commented', 'seed', '%s', '2026-10-01 00:00:03')",
			strings.Repeat("e", 70000)), "events.comment")
		expectTooLarge(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO wisp_events (id, issue_id, event_type, actor, comment, created_at) VALUES ('m75-wevt-big', 'm75-wisp', 'commented', 'seed', '%s', '2026-10-01 00:00:04')",
			strings.Repeat("w", 70000)), "wisp_events.comment")
		expectTooLarge(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO issue_snapshots (id, issue_id, snapshot_time, compaction_level, original_size, compressed_size, original_content) VALUES ('m75-snap-big', 'm75-issue', '2026-10-01 00:00:00', 1, 70000, 70000, '%s')",
			strings.Repeat("s", 70000)), "issue_snapshots.original_content")
		expectTooLarge(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO compaction_snapshots (id, issue_id, compaction_level, snapshot_json) VALUES ('m75-comp-big', 'm75-issue', 1, UNHEX(REPEAT('ab', 70001)))"),
			"compaction_snapshots.snapshot_json")

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp from 74: %v", err)
		}

		// All five columns at their specified definitions: type, nullability
		// and default, exactly as the migration's MODIFYs restate them.
		assertColumn(t, ctx, conn, "events", "comment", "longtext", "YES", sql.NullString{})
		assertColumn(t, ctx, conn, "wisp_events", "comment", "longtext", "YES", sql.NullString{String: "", Valid: true})
		assertColumn(t, ctx, conn, "issue_snapshots", "original_content", "longtext", "NO", sql.NullString{})
		assertColumn(t, ctx, conn, "issue_snapshots", "archived_events", "longtext", "YES", sql.NullString{})
		assertColumn(t, ctx, conn, "compaction_snapshots", "snapshot_json", "longblob", "NO", sql.NullString{})

		// Existing values retained, NULLs included.
		if got := scalarInt(t, ctx, conn, "SELECT CASE WHEN comment IS NULL THEN 1 ELSE 0 END FROM events WHERE id = 'm75-evt-null'"); got != 1 {
			t.Fatalf("m75-evt-null comment IS NULL = %d, want 1", got)
		}
		if got := scalarString(t, ctx, conn, "SELECT comment FROM events WHERE id = 'm75-evt-small'"); got != "kept small comment" {
			t.Fatalf("small events.comment = %q, want retained", got)
		}
		if got := scalarString(t, ctx, conn, "SELECT comment FROM wisp_events WHERE id = 'm75-wevt'"); got != "kept wisp comment" {
			t.Fatalf("wisp_events.comment = %q, want retained", got)
		}
		if got := scalarString(t, ctx, conn, "SELECT original_content FROM issue_snapshots WHERE id = 'm75-snap'"); got != "kept snapshot body" {
			t.Fatalf("issue_snapshots.original_content = %q, want retained", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT CASE WHEN archived_events IS NULL THEN 1 ELSE 0 END FROM issue_snapshots WHERE id = 'm75-snap'"); got != 1 {
			t.Fatalf("NULL issue_snapshots.archived_events did not stay NULL")
		}
		if got := scalarString(t, ctx, conn, "SELECT HEX(snapshot_json) FROM compaction_snapshots WHERE id = 'm75-comp'"); got != "00FF10AA20BB" {
			t.Fatalf("compaction_snapshots.snapshot_json = %s, want retained binary bytes", got)
		}

		// Oversized writes now round-trip byte-for-byte. The refused inserts
		// left nothing behind.
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id = 'm75-evt-big'"); got != 0 {
			t.Fatalf("refused oversized insert left %d rows behind", got)
		}
		execFrozenGuard(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO events (id, issue_id, event_type, actor, comment, created_at) VALUES ('m75-evt-big', 'm75-issue', 'commented', 'seed', '%s', '2026-10-01 00:00:05')",
			strings.Repeat("e", 70000)))
		if got := scalarInt(t, ctx, conn, "SELECT LENGTH(comment) FROM events WHERE id = 'm75-evt-big'"); got != 70000 {
			t.Fatalf("oversized events.comment length = %d, want 70000", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT CASE WHEN comment = REPEAT('e', 70000) THEN 1 ELSE 0 END FROM events WHERE id = 'm75-evt-big'"); got != 1 {
			t.Fatalf("oversized events.comment bytes differ from REPEAT('e', 70000)")
		}
		execFrozenGuard(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO wisp_events (id, issue_id, event_type, actor, comment, created_at) VALUES ('m75-wevt-big', 'm75-wisp', 'commented', 'seed', '%s', '2026-10-01 00:00:06')",
			strings.Repeat("w", 70000)))
		if got := scalarInt(t, ctx, conn, "SELECT LENGTH(comment) FROM wisp_events WHERE id = 'm75-wevt-big'"); got != 70000 {
			t.Fatalf("oversized wisp_events.comment length = %d, want 70000", got)
		}
		execFrozenGuard(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO compaction_snapshots (id, issue_id, compaction_level, snapshot_json) VALUES ('m75-comp-big', 'm75-issue', 1, UNHEX(REPEAT('ab', 70000)))"))
		if got := scalarInt(t, ctx, conn, "SELECT LENGTH(snapshot_json) FROM compaction_snapshots WHERE id = 'm75-comp-big'"); got != 70000 {
			t.Fatalf("oversized snapshot_json length = %d, want 70000", got)
		}

		// Replay of both frozen texts on the same database is a no-op: same
		// definitions, same rows.
		execFrozenGuard(t, ctx, conn, migrationSQL)
		execFrozenGuard(t, ctx, conn, ignoredSQL)
		assertColumn(t, ctx, conn, "events", "comment", "longtext", "YES", sql.NullString{})
		assertColumn(t, ctx, conn, "wisp_events", "comment", "longtext", "YES", sql.NullString{String: "", Valid: true})
		assertColumn(t, ctx, conn, "issue_snapshots", "original_content", "longtext", "NO", sql.NullString{})
		assertColumn(t, ctx, conn, "issue_snapshots", "archived_events", "longtext", "YES", sql.NullString{})
		assertColumn(t, ctx, conn, "compaction_snapshots", "snapshot_json", "longblob", "NO", sql.NullString{})
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id IN ('m75-evt-null', 'm75-evt-small', 'm75-evt-big')"); got != 3 {
			t.Fatalf("events rows after replay = %d, want 3", got)
		}
		if got := scalarString(t, ctx, conn, "SELECT comment FROM events WHERE id = 'm75-evt-small'"); got != "kept small comment" {
			t.Fatalf("small events.comment changed on replay: %q", got)
		}
	})

	t.Run("mixed issue_snapshots widths converge independently", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			widen string
		}{
			{"original_content already wide", "ALTER TABLE issue_snapshots MODIFY COLUMN original_content LONGTEXT NOT NULL"},
			{"archived_events already wide", "ALTER TABLE issue_snapshots MODIFY COLUMN archived_events LONGTEXT"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dataDir := seedMainSchemaAt(t, ctx, 74)
				conn, closeConn := openPinnedConn(t, ctx, dataDir)
				defer closeConn()

				execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('m75-mix', 'mix', '', '', '', '', 'open', 2, 'task');
INSERT INTO issue_snapshots (id, issue_id, snapshot_time, compaction_level, original_size, compressed_size, original_content, archived_events)
VALUES ('m75-mix-snap', 'm75-mix', '2026-10-01 00:00:00', 1, 15, 15, 'mix snapshot body', 'mix events tail');
`)
				execFrozenGuard(t, ctx, conn, tc.widen)

				execFrozenGuard(t, ctx, conn, migrationSQL)

				assertColumn(t, ctx, conn, "issue_snapshots", "original_content", "longtext", "NO", sql.NullString{})
				assertColumn(t, ctx, conn, "issue_snapshots", "archived_events", "longtext", "YES", sql.NullString{})
				if got := scalarString(t, ctx, conn, "SELECT original_content FROM issue_snapshots WHERE id = 'm75-mix-snap'"); got != "mix snapshot body" {
					t.Fatalf("original_content = %q, want retained through convergence", got)
				}
				if got := scalarString(t, ctx, conn, "SELECT archived_events FROM issue_snapshots WHERE id = 'm75-mix-snap'"); got != "mix events tail" {
					t.Fatalf("archived_events = %q, want retained through convergence", got)
				}

				// Idempotent: a second pass over the now-uniform shape.
				execFrozenGuard(t, ctx, conn, migrationSQL)
				assertColumn(t, ctx, conn, "issue_snapshots", "original_content", "longtext", "NO", sql.NullString{})
				assertColumn(t, ctx, conn, "issue_snapshots", "archived_events", "longtext", "YES", sql.NullString{})
			})
		}
	})

	t.Run("absent event tables leave the snapshot widenings firing", func(t *testing.T) {
		dataDir := seedMainSchemaAt(t, ctx, 74)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS events")
		execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS wisp_events")

		execFrozenGuard(t, ctx, conn, migrationSQL)

		for _, table := range []string{"events", "wisp_events"} {
			if got := scalarInt(t, ctx, conn, fmt.Sprintf(
				"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s'", table)); got != 0 {
				t.Fatalf("%s reappeared from a guarded no-op", table)
			}
		}
		assertColumn(t, ctx, conn, "issue_snapshots", "original_content", "longtext", "NO", sql.NullString{})
		assertColumn(t, ctx, conn, "issue_snapshots", "archived_events", "longtext", "YES", sql.NullString{})
		assertColumn(t, ctx, conn, "compaction_snapshots", "snapshot_json", "longblob", "NO", sql.NullString{})
	})

	t.Run("absent snapshot tables leave the event widenings firing", func(t *testing.T) {
		dataDir := seedMainSchemaAt(t, ctx, 74)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS issue_snapshots")
		execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS compaction_snapshots")

		execFrozenGuard(t, ctx, conn, migrationSQL)

		for _, table := range []string{"issue_snapshots", "compaction_snapshots"} {
			if got := scalarInt(t, ctx, conn, fmt.Sprintf(
				"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s'", table)); got != 0 {
				t.Fatalf("%s reappeared from a guarded no-op", table)
			}
		}
		assertColumn(t, ctx, conn, "events", "comment", "longtext", "YES", sql.NullString{})
		assertColumn(t, ctx, conn, "wisp_events", "comment", "longtext", "YES", sql.NullString{String: "", Valid: true})
	})
}

// expectTooLarge asserts the engine refuses an oversized write rather than
// truncating it, matching the pinned go-mysql-server behavior for plain
// INSERTs (ErrLengthBeyondLimit, Error 1105).
func expectTooLarge(t *testing.T, ctx context.Context, conn *sql.Conn, insertSQL, target string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, insertSQL); err == nil {
		t.Fatalf("oversized write into %s was accepted at v74; the TEXT/BLOB cap must refuse it", target)
	} else if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized write into %s failed with %v; want the too-large column error", target, err)
	}
}

// assertColumn pins one column's full definition in INFORMATION_SCHEMA.
func assertColumn(t *testing.T, ctx context.Context, conn *sql.Conn, table, column, wantType, wantNullable string, wantDefault sql.NullString) {
	t.Helper()
	var gotType, gotNullable string
	var gotDefault sql.NullString
	err := conn.QueryRowContext(ctx, `
SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&gotType, &gotNullable, &gotDefault)
	if err != nil {
		t.Fatalf("read %s.%s definition: %v", table, column, err)
	}
	if gotType != wantType || gotNullable != wantNullable || gotDefault != wantDefault {
		t.Fatalf("%s.%s = {type=%s nullable=%s default=%v}, want {type=%s nullable=%s default=%v}",
			table, column, gotType, gotNullable, gotDefault, wantType, wantNullable, wantDefault)
	}
}
