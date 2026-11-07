//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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

// seedWispHealGraph writes the dependency shape whose flags only a live
// recompute can restore: durable open blocker A; wisp B blocked by A; wisp C,
// B's child, blocked through its parent; and independent wisp D, all with
// their flags already maintained.
func seedWispHealGraph(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
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
}

// driftWispHealAndRewind removes the shape ignored/0006 added -- index first,
// then the column it is keyed on, the way the drifted clones in the wild
// present -- and rewinds the ignored cursor past the 0032 marker so the next
// open re-pends it with its repair.
func driftWispHealAndRewind(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	execFrozenGuard(t, ctx, conn, `
DROP INDEX idx_wisps_is_blocked ON wisps;
ALTER TABLE wisps DROP COLUMN is_blocked;
`)
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version >= 32")
}

// requireWispHealShapeFlags asserts the restored shape and the recomputed
// flags: the column back with ignored/0006's definition, B and C blocked, D
// clear -- not the column's DEFAULT 0.
func requireWispHealShapeFlags(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
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
}

// TestEmbeddedWispIsBlockedRestoreAtIgnored32 seeds the dependency shape whose
// flags only a recompute can restore, drifts the wisps plane back to the
// pre-0006 shape, rewinds the ignored cursor past 0032, and proves the next
// open restores the column AND recomputes every flag from the live graph,
// leaving the durable plane alone.
func TestEmbeddedWispIsBlockedRestoreAtIgnored32(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("MigrateUp (seed): %v", err)
	}
	seedWispHealGraph(t, ctx, conn)
	driftWispHealAndRewind(t, ctx, conn)
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
		requireWispHealShapeFlags(t, ctx, conn)
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

// TestEmbeddedWispIsBlockedRepairFailureRetries is the embedded twin of the
// server-mode fault test: a fault at any stage of the restore must fail the
// embedded open with the fault surfaced, record no ignored 0032 row (the
// marker stays pending), and the next open with the fault cleared must resume
// the repair to completion -- shape, recomputed flags, exactly one 32. The
// stage strings mirror schema's wispIsBlockedRestoreAfter* constants as
// literals, so a drift between the two fails here rather than silently
// skipping.
func TestEmbeddedWispIsBlockedRepairFailureRetries(t *testing.T) {
	requireEmbedded(t)

	for _, stage := range []string{"after_column", "after_recompute", "after_index"} {
		t.Run(stage, func(t *testing.T) {
			ctx := t.Context()

			dataDir := seedMainSchemaAt(t, ctx, 61)
			beadsDir := filepath.Dir(dataDir)
			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				closeConn()
				t.Fatalf("MigrateUp (seed): %v", err)
			}
			seedWispHealGraph(t, ctx, conn)
			driftWispHealAndRewind(t, ctx, conn)
			closeConn()

			fault := errors.New("injected fault")
			restore := schema.SetWispIsBlockedRestoreFaultHookForTest(func(at string) error {
				if at == stage {
					return fault
				}
				return nil
			})
			_, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
			restore()
			if err == nil {
				t.Fatalf("open with a fault pinned at %s must fail and leave 0032 pending", stage)
			}
			if !strings.Contains(err.Error(), "wisp is_blocked restore fault at "+stage) {
				t.Fatalf("open error = %v, want the %s fault surfaced", err, stage)
			}

			withRawConn(t, dataDir, "testdb", func(conn *sql.Conn) {
				var recorded int
				if err := conn.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32").Scan(&recorded); err != nil {
					t.Fatalf("read ignored cursor: %v", err)
				}
				if recorded != 0 {
					t.Fatalf("failed pass recorded ignored 0032 %d times, want 0 (the repair must stay pending for retry)", recorded)
				}
			})

			store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
			if err != nil {
				t.Fatalf("open after the fault cleared: %v", err)
			}
			closeEmbeddedDoltStore(t, store)
			withRawConn(t, dataDir, "testdb", func(conn *sql.Conn) {
				requireWispHealShapeFlags(t, ctx, conn)
				if !ignoredCursorHas(t, ctx, conn, 32) {
					t.Fatal("ignored cursor does not record 0032 after the retried restore")
				}
			})
		})
	}
}
