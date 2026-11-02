//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin the pre-flip events restore (gh 7039, ignored migration
// 0031's Go repair): main migration 0062 commits the versioned events
// drop, the drop replicates to every pulling clone, but the dolt_ignored
// recreate does not — so ignored/0019's replay re-creates the table EMPTY
// and the clone's pre-flip audit history (what `bd history --events` reads)
// stays reachable in the clone's own commit history while being invisible
// to every reader. The repair resolves the drop commit's first parent and
// re-inserts the missing rows under content-derived ids.
//
// They run through the real embedded (cgo) engine for the same reason the
// 0062 flip tests do: the restore reads `AS OF` history through
// dolt_log/dolt_commit_ancestors and writes working-set rows on an ignored
// table, none of which sqlmock or the dolt CLI batch executor exercise
// faithfully. All require BEADS_TEST_EMBEDDED_DOLT=1 (requireEmbedded,
// migrate_selfheal_test.go).

// ns makes a non-NULL digest field; a zero sql.NullString is NULL, and the
// digest encoding keeps the two distinct.
func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// eventNull is eventDigest's NULL sentinel for the three nullable text
// columns (the digest encoding keeps NULL and the empty string distinct).
const eventNull = "\x00null"

// eventDigest mirrors the frozen events content-digest column list (the aux
// row-id rekey's events entry): issue_id, event_type, actor, old_value,
// new_value, comment, created_at rendered as 'YYYY-MM-DD HH:MM:SS'.
func eventDigest(issueID, eventType, actor, oldValue, newValue, comment, createdAt string) string {
	var old, newV, com sql.NullString
	if oldValue != eventNull {
		old = ns(oldValue)
	}
	if newValue != eventNull {
		newV = ns(newValue)
	}
	if comment != eventNull {
		com = ns(comment)
	}
	return rowid.Digest([]sql.NullString{ns(issueID), ns(eventType), ns(actor), old, newV, com, ns(createdAt)})
}

// seedPreflipEvents commits an issue plus its pre-flip audit rows on the
// still-versioned plane at main v61, returning nothing: the commit is the
// history the restore must later read.
func seedPreflipEvents(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string, events string) {
	t.Helper()
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('`+issueID+`', 'preflip seed', '', '', '', '', 'open', 2, 'task');
`+events)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed committed events rows')")
}

// commitWorkingSet stages and commits the current working set, the same
// shape seedMainSchemaAt uses for its baseline.
func commitWorkingSet(t *testing.T, ctx context.Context, conn *sql.Conn, message string) {
	t.Helper()
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', '"+message+"')")
}

// TestEmbeddedIgnoredMigration0019SeedsEventsFromPreFlipHistory is the gh
// 7039 repro: the migrator runs the full chain (0062 preserves its own rows;
// the ignored tail records the cursor without duplicating them), then the
// pulling clone's working set loses the never-replicated events table and
// the next open must restore the committed pre-flip rows — multiplicities
// included — under content-derived ids.
func TestEmbeddedIgnoredMigration0019SeedsEventsFromPreFlipHistory(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	seedPreflipEvents(t, ctx, conn, "adopt-1", `
INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES
('adopt-1', 'created', 'tester', NULL, NULL, NULL, '2026-07-30 00:00:00', 'evt-adopt-1'),
('adopt-1', 'status_changed', 'tester', 'open', 'in_progress', NULL, '2026-07-30 00:01:00', 'evt-adopt-2'),
('adopt-1', 'status_changed', 'tester', 'open', 'in_progress', NULL, '2026-07-30 00:01:00', 'evt-adopt-3');
`)

	// The migrator's own pass: rows preserved through the flip, not duplicated
	// by the restore (its live content already equals the historical source).
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id LIKE 'evt-adopt-%'"); got != 3 {
		t.Fatalf("post-migrator original events rows = %d, want 3 (restore must not duplicate content the flip preserved)", got)
	}

	// The pulling clone: the committed drop arrives via pull; the working-set
	// table (which never replicated) is gone. The next open replays ignored
	// 0019 (empty re-create) .. 0031 (restore).
	execFrozenGuard(t, ctx, conn, "DROP TABLE events")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (adopting clone): %v", err)
	}

	createdDigest := eventDigest("adopt-1", "created", "tester", eventNull, eventNull, eventNull, "2026-07-30 00:00:00")
	statusDigest := eventDigest("adopt-1", "status_changed", "tester", "open", "in_progress", eventNull, "2026-07-30 00:01:00")

	// Content multiplicities restored: one created row, the status_changed
	// duplicate pair, each under its lowest-ordinal derived id.
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id = ?", rowid.New("events", 0, createdDigest)); got != 1 {
		t.Fatalf("restored created row under derived id ord 0 = %d, want 1", got)
	}
	for ord := 0; ord < 2; ord++ {
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id = ?", rowid.New("events", ord, statusDigest)); got != 1 {
			t.Fatalf("restored status_changed row under derived id ord %d = %d, want 1 (duplicate multiplicity)", ord, got)
		}
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 3 {
		t.Fatalf("restored events rows = %d, want 3", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id LIKE 'evt-adopt-%'"); got != 0 {
		t.Fatalf("legacy pre-flip ids present after restore = %d, want 0 (restore mints derived ids)", got)
	}

	// The restore completes the ignored cursor and stays off dolt_status.
	if v, err := schema.CurrentIgnoredVersion(ctx, conn); err != nil {
		t.Fatalf("CurrentIgnoredVersion: %v", err)
	} else if v != schema.LatestIgnoredVersion() {
		t.Fatalf("ignored cursor = %d, want latest %d", v, schema.LatestIgnoredVersion())
	}
	assertEventsAbsentFromStatus(t, ctx, conn)
}
