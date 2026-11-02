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
// TestEmbeddedIgnoredMigration0031PreservesExistingRowsAndIDs pins the
// restore's reconciliation contract on a table that already holds rows: ids
// and rows that exist are never touched or re-ided, only the missing content
// multiplicity is added, and live-only rows (post-flip audit writes whose
// content never existed in history) survive untouched.
func TestEmbeddedIgnoredMigration0031PreservesExistingRowsAndIDs(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	seedPreflipEvents(t, ctx, conn, "keep-1", `
INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES
('keep-1', 'created', 'tester', NULL, NULL, NULL, '2026-07-30 00:00:00', 'evt-keep-1'),
('keep-1', 'status_changed', 'tester', 'open', 'in_progress', NULL, '2026-07-30 00:01:00', 'evt-keep-2'),
('keep-1', 'status_changed', 'tester', 'open', 'in_progress', NULL, '2026-07-30 00:01:00', 'evt-keep-3');
`)

	// Run the flip (main 62..74) and commit, leaving the ignored series
	// pending: the clone's events table holds the flip-preserved rows.
	if _, err := schema.MigrateUpTo(ctx, conn, 74); err != nil {
		t.Fatalf("MigrateUpTo(74): %v", err)
	}
	commitWorkingSet(t, ctx, conn, "test: flipped schema")

	// A partial hand-restore lost one of the duplicate pair, and a post-flip
	// audit write (content that never existed in history) landed locally.
	execFrozenGuard(t, ctx, conn, "DELETE FROM events WHERE id = 'evt-keep-3'")
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id)
VALUES ('keep-1', 'local_write', 'tester', NULL, NULL, NULL, '2026-08-01 00:00:00', 'evt-live-only');
`)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	// Existing rows and ids survive verbatim.
	for _, id := range []string{"evt-keep-1", "evt-keep-2", "evt-live-only"} {
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id = ?", id); got != 1 {
			t.Fatalf("pre-existing row %s = %d, want 1 (restore must not touch or re-id existing rows)", id, got)
		}
	}
	// Only the missing multiplicity is added, under a derived id (ordinals 0
	// and 1 are free: the surviving duplicate holds a legacy id).
	dupDigest := eventDigest("keep-1", "status_changed", "tester", "open", "in_progress", eventNull, "2026-07-30 00:01:00")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id = ?", rowid.New("events", 0, dupDigest)); got != 1 {
		t.Fatalf("restored missing duplicate under derived id = %d, want 1", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 4 {
		t.Fatalf("events rows after restore = %d, want 4 (3 preserved + 1 restored)", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE event_type = 'created' AND id = 'evt-keep-1'"); got != 1 {
		t.Fatalf("created row preserved under its original id = %d, want 1", got)
	}
	assertEventsAbsentFromStatus(t, ctx, conn)
}

// TestEmbeddedPreflipSeedSkipsEventsOfGoneIssues pins the orphan rule: an
// issue deleted after the flip is gone from the live issues table, so its
// historical events must be skipped (never re-inserted, or the foreign key
// would refuse the restore), while live issues get their history back.
func TestEmbeddedPreflipSeedSkipsEventsOfGoneIssues(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	seedPreflipEvents(t, ctx, conn, "live-1", `
INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES
('live-1', 'created', 'tester', NULL, NULL, NULL, '2026-07-30 00:00:00', 'evt-live-1');
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('gone-1', 'deleted after flip', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES
('gone-1', 'created', 'tester', NULL, NULL, NULL, '2026-07-30 00:03:00', 'evt-gone-1');
`)

	if _, err := schema.MigrateUpTo(ctx, conn, 74); err != nil {
		t.Fatalf("MigrateUpTo(74): %v", err)
	}
	commitWorkingSet(t, ctx, conn, "test: flipped schema")

	// The issue is deleted after the flip (cascading its live events away)
	// and the working-set table is lost — the adopter shape.
	execFrozenGuard(t, ctx, conn, "DELETE FROM issues WHERE id = 'gone-1'")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE issue_id = 'gone-1'"); got != 0 {
		t.Fatalf("cascade pre-check: gone-1 live events = %d, want 0", got)
	}
	execFrozenGuard(t, ctx, conn, "DROP TABLE events")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v (restoring a deleted issue's events would have failed the foreign key)", err)
	}

	liveDigest := eventDigest("live-1", "created", "tester", eventNull, eventNull, eventNull, "2026-07-30 00:00:00")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id = ?", rowid.New("events", 0, liveDigest)); got != 1 {
		t.Fatalf("restored live issue's events = %d, want 1", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE issue_id = 'gone-1'"); got != 0 {
		t.Fatalf("restored events of deleted issue = %d, want 0 (orphans are skipped)", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 1 {
		t.Fatalf("events rows after restore = %d, want 1", got)
	}
	assertEventsAbsentFromStatus(t, ctx, conn)
}

// TestEmbeddedPreflipSeedCompletesWithoutSourceSilently pins the no-source
// shape: a database whose flip commit dropped an EMPTY table (the shape a
// fresh install or a post-squash re-clone presents) records the restore's
// completion silently — no rows are invented and the cursor advances.
func TestEmbeddedPreflipSeedCompletesWithoutSourceSilently(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	// Full chain from scratch: 0062 flips an empty events table, so the
	// drop commit's parent carries the table with zero rows.
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 0 {
		t.Fatalf("events rows after restore = %d, want 0 (nothing to restore, nothing invented)", got)
	}
	// The restore's version is recorded: this pass, and every later open,
	// must see the work done rather than re-reading history each time.
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 31"); got != 1 {
		t.Fatalf("ignored_schema_migrations row for the restore version 31 = %d, want 1", got)
	}
	assertEventsAbsentFromStatus(t, ctx, conn)
}

