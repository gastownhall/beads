//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// This file twins the wisps.is_blocked restore onto embedded dolt
// (gastownhall/beads#4468, #5257): the same drift shape and the same ignored
// cursor rewind as the server-mode twin in internal/storage/dolt, healed
// through embeddeddolt.Open -- the open path bd itself takes -- and not
// through a direct MigrateUp call, because the defect is an open-path failure
// and the fix must be proven on the same path. Like the rest of the embedded
// suite it needs BEADS_TEST_EMBEDDED_DOLT=1.

// TestEmbeddedWispIsBlockedRestoreAtIgnored32 seeds the dependency shape whose
// flags only a recompute can restore (durable open blocker A; wisp B blocked
// by A; wisp C, B's child, blocked through its parent; independent wisp D),
// drifts the wisps plane back to the pre-0006 shape, rewinds the ignored
// cursor past 0032, and proves the next open restores the column AND
// recomputes every flag from the live graph, leaving the durable plane alone.
func TestEmbeddedWispIsBlockedRestoreAtIgnored32(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("MigrateUp (seed): %v", err)
	}
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('wh-a', 'durable blocker', '', '', '', '', 'open', 2, 'task');
INSERT INTO wisps (id, title, status, priority, issue_type, ephemeral, is_blocked, created_at, updated_at)
VALUES ('wh-b', 'blocked wisp', 'open', 2, 'task', 1, 1, NOW(), NOW()),
       ('wh-c', 'child wisp', 'open', 2, 'task', 1, 1, NOW(), NOW()),
       ('wh-d', 'free wisp', 'open', 2, 'task', 1, 0, NOW(), NOW());
`)
	// Dependency ids are derived from the natural edge key the way the
	// production add path derives them (depid.New), so the rows carry the
	// shape every other clone of this store would agree on.
	mustExecOn(t, ctx, conn, "INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)"+
		" VALUES (?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())", depid.New("wh-b", "wh-a"), "wh-b", "wh-a")
	mustExecOn(t, ctx, conn, "INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)"+
		" VALUES (?, ?, ?, 'parent-child', NOW(), 'tester', JSON_OBJECT())", depid.New("wh-c", "wh-b"), "wh-c", "wh-b")
	// The drift the wild clones present (index first, then the column it is
	// keyed on) plus the cursor rewind that re-pends the marker.
	execFrozenGuard(t, ctx, conn, `
DROP INDEX idx_wisps_is_blocked ON wisps;
ALTER TABLE wisps DROP COLUMN is_blocked;
`)
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version >= 32")
	closeConn()

	store, err := embeddeddolt.Open(ctx, filepath.Dir(dataDir), "testdb", "main")
	if err != nil {
		t.Fatalf("open after drift: %v", err)
	}
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{IncludeEphemeral: true})
	closeEmbeddedDoltStore(t, store)
	if err != nil {
		t.Fatalf("GetReadyWork after reopen: %v (the reported symptom: a drifted clone cannot serve ready queries)", err)
	}
	readyIDs := map[string]bool{}
	for _, is := range ready {
		readyIDs[is.ID] = true
	}
	for id, want := range map[string]bool{"wh-a": true, "wh-d": true, "wh-b": false, "wh-c": false} {
		if readyIDs[id] != want {
			t.Fatalf("ready work holds %s=%v, want %v (ready set: %v)", id, readyIDs[id], want, readyIDs)
		}
	}

	withRawConn(t, dataDir, "testdb", func(conn *sql.Conn) {
		var shape string
		if err := conn.QueryRowContext(ctx, `SELECT CONCAT(COLUMN_TYPE, '|', IS_NULLABLE, '|', COALESCE(COLUMN_DEFAULT, '<null>'))
			FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`).Scan(&shape); err != nil {
			t.Fatalf("read wisps.is_blocked shape: %v", err)
		}
		if shape != "tinyint(1)|NO|0" {
			t.Fatalf("wisps.is_blocked shape = %s, want tinyint(1)|NO|0", shape)
		}
		flags := map[string]bool{}
		rows, err := conn.QueryContext(ctx, "SELECT id, is_blocked FROM wisps")
		if err != nil {
			t.Fatalf("read wisp flags: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var blocked bool
			if err := rows.Scan(&id, &blocked); err != nil {
				t.Fatalf("scan wisp flag: %v", err)
			}
			flags[id] = blocked
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read wisp flags: %v", err)
		}
		for id, want := range map[string]bool{"wh-b": true, "wh-c": true, "wh-d": false} {
			if flags[id] != want {
				t.Fatalf("healed flag for %s = %v, want %v: the restore must recompute, not default to 0", id, flags[id], want)
			}
		}
		var issueBlocked bool
		if err := conn.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = 'wh-a'").Scan(&issueBlocked); err != nil {
			t.Fatalf("read durable blocker flag: %v", err)
		}
		if issueBlocked {
			t.Fatal("the durable blocker wh-a was flagged; the restore must not touch the durable plane")
		}
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Fatal("ignored cursor does not record 0032 after the restore")
		}
	})
}
