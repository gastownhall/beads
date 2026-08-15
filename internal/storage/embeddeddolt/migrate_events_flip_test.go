//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin the bd-red8u events-table plane flip (main migration 0062 +
// ignored migration 0019): the events audit table leaves committed history
// and lives dolt_ignored in the working set, Protocol v0.1 C2.2's
// no-per-write-history-cost rule.
//
// Like the other frozen-guard tests in this file's siblings, they run the
// exact frozen migration bytes through a real embedded (cgo) engine — 0062's
// conditional DOLT_ADD is PREPARE-driven and its DOLT_COMMIT is a real
// stored-procedure call, neither of which the dolt CLI batch executor
// exercises faithfully. Both require BEADS_TEST_EMBEDDED_DOLT=1
// (requireEmbedded, migrate_selfheal_test.go).

// TestEmbeddedMigration0062FlipsEventsToIgnoredPlane drives the full
// tracked->ignored flip on a database whose events table is genuinely
// committed with rows, through the production migration path, and then
// proves the three contract points: the drop is committed out of HEAD, the
// rows survive in the working set, and subsequent event writes neither dirty
// dolt_status nor become stageable. A full replay of the frozen bytes (the
// interrupted-pass retry shape) must converge without error or a second
// commit.
func TestEmbeddedMigration0062FlipsEventsToIgnoredPlane(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// Seed a committed issue + audit event on the (still versioned) plane.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('flip-1', 'flip seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('flip-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-seed-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed committed events row')")

	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events AS OF 'HEAD'"); got != 1 {
		t.Fatalf("pre-flip committed events = %d, want 1", got)
	}
	headBefore := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")

	// The flip, through the production path (execMigrationBody -> DrainCall).
	if _, err := schema.MigrateUpTo(ctx, conn, 62); err != nil {
		t.Fatalf("MigrateUpTo(62): %v", err)
	}

	// 1. The drop is committed: HEAD moved and no longer carries events.
	headAfter := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	if headAfter == headBefore {
		t.Fatal("flip did not commit the events drop (HEAD unchanged)")
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM events AS OF 'HEAD'").Scan(new(int)); err == nil {
		t.Fatal("events still exists at HEAD; want it dropped from committed history")
	}

	// 2. Rows survive in the working set, and the ignore pattern is present.
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 1 {
		t.Fatalf("post-flip working-set events = %d, want 1", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = 'events' AND ignored = 1"); got != 1 {
		t.Fatalf("dolt_ignore rows for events = %d, want 1", got)
	}

	// 3. New audit writes are invisible to dolt_status and unstageable: the
	// runtime's explicit DOLT_ADD('events') (issues.go staging lists) must
	// silently no-op, minting nothing on a subsequent commit.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('flip-1', 'updated', 'tester', '2026-07-30 00:00:01', 'evt-post-flip');
`)
	assertEventsAbsentFromStatus(t, ctx, conn)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('events')")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'events' AND staged = 1"); got != 0 {
		t.Fatalf("explicit DOLT_ADD staged the ignored events table (%d rows staged)", got)
	}

	// 4. Replay of the frozen bytes converges: no error, no new commit, both
	// rows preserved (the temp-copy union keeps the pre-crash copy).
	flipSQL, err := schema.MigrationSQL("0062_events_dolt_ignore.up.sql")
	if err != nil {
		t.Fatalf("read 0062 migration: %v", err)
	}
	headBeforeReplay := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	if err := schema.DrainCall(ctx, conn, flipSQL); err != nil {
		t.Fatalf("replaying 0062 frozen bytes: %v", err)
	}
	if head := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1"); head != headBeforeReplay {
		t.Fatal("0062 replay minted a commit; --skip-empty must make it a no-op")
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 2 {
		t.Fatalf("post-replay events rows = %d, want 2", got)
	}
	assertEventsAbsentFromStatus(t, ctx, conn)
}

// TestEmbeddedIgnoredMigration0019CreatesEventsForFreshClones simulates the
// fresh-clone door: a database whose main cursor is at-latest (0062 recorded
// in committed history) but which arrived without the working-set-only events
// table. The full MigrateUp pass must materialize it via ignored/0019, and a
// replay of 0019's frozen bytes must never touch an existing table.
func TestEmbeddedIgnoredMigration0019CreatesEventsForFreshClones(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// A clone materializes committed history only; the ignored-plane events
	// table (recreated by 0062 in the working set) is not part of it.
	execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS events")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 0 {
		t.Fatalf("fresh events table row count = %d, want 0", got)
	}
	assertEventsAbsentFromStatus(t, ctx, conn)

	// Replay branch: with the table present (now holding a row), the frozen
	// bytes must leave it untouched.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('clone-1', 'clone seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('clone-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-clone-1');
`)
	createSQL, err := schema.IgnoredMigrationSQL("0019_create_events.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0019 migration: %v", err)
	}
	execFrozenGuard(t, ctx, conn, createSQL)
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 1 {
		t.Fatalf("events rows after 0019 replay = %d, want 1 (existing table must not be touched)", got)
	}

	// The FK contract survives the ignored-lane creation: an event for a
	// missing issue must be refused (fk_events_issue crosses to the
	// versioned issues table).
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES ('no-such-issue', 'created', 'tester', '2026-07-30 00:00:00', 'evt-orphan')",
	); err == nil {
		t.Fatal("orphan event insert succeeded; want fk_events_issue to refuse it")
	}
}

func mustDrain(t *testing.T, ctx context.Context, conn *sql.Conn, sqlText string) {
	t.Helper()
	if err := schema.DrainCall(ctx, conn, sqlText); err != nil {
		t.Fatalf("drain %q: %v", sqlText, err)
	}
}

func scalarInt(t *testing.T, ctx context.Context, conn *sql.Conn, query string) int {
	t.Helper()
	var v int
	if err := conn.QueryRowContext(ctx, query).Scan(&v); err != nil {
		t.Fatalf("scalar %q: %v", query, err)
	}
	return v
}

func scalarString(t *testing.T, ctx context.Context, conn *sql.Conn, query string) string {
	t.Helper()
	var v string
	if err := conn.QueryRowContext(ctx, query).Scan(&v); err != nil {
		t.Fatalf("scalar %q: %v", query, err)
	}
	return v
}

func assertEventsAbsentFromStatus(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	tables, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	for _, name := range tables {
		if strings.EqualFold(name, "events") {
			t.Fatalf("events present in dolt_status (%v); the ignored plane must suppress it", tables)
		}
	}
}
