//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin the restore of the pre-upgrade events audit history on
// clones that did not run main migration 0062 themselves
// (gastownhall/beads#7039). 0062 commits the DROP of the versioned events
// table and recreates it only in the dolt_ignored working set, so every clone
// that adopts it by pull -- or clones after it -- used to get an empty table
// from ignored/0019 while the rows sat in its own history. Ignored migration
// 0031's repair restores them from the first parent of the drop commit. Like
// the rest of the events-flip tests they need BEADS_TEST_EMBEDDED_DOLT=1.

const preflipDatabase = "testdb"

// preflipEventDigestColumns is the content the restore reconciles by: the
// frozen aux row-id digest columns.
const preflipEventDigestColumns = "issue_id, event_type, actor, old_value, new_value, comment, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')"

// seedPreflipEvents commits, on a database at main 61 (events still
// versioned), two issues and their audit rows under legacy (pre-rekey) ids:
// an exact duplicate pair, and NULL next to empty-string values, which the
// content digest must keep apart.
func seedPreflipEvents(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('pre-1', 'first', '', '', '', '', 'open', 2, 'task'),
  ('pre-2', 'second', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES
  ('pre-1', 'created', 'alice', NULL, NULL, NULL, '2026-07-01 10:00:00', 'legacy-1'),
  ('pre-1', 'commented', 'bob', NULL, NULL, 'same words', '2026-07-01 11:00:00', 'legacy-2'),
  ('pre-1', 'commented', 'bob', NULL, NULL, 'same words', '2026-07-01 11:00:00', 'legacy-3'),
  ('pre-1', 'status_changed', 'alice', NULL, '', NULL, '2026-07-01 12:00:00', 'legacy-4'),
  ('pre-2', 'updated', 'carol', '', NULL, '', '2026-07-02 09:00:00', 'legacy-5');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed pre-flip events')")
}

// preflipSeededContent is seedPreflipEvents' rows as digest -> multiplicity.
func preflipSeededContent() map[string]int {
	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	null := sql.NullString{}
	d := func(f ...sql.NullString) string { return rowid.Digest(f) }
	return map[string]int{
		d(v("pre-1"), v("created"), v("alice"), null, null, null, v("2026-07-01 10:00:00")):            1,
		d(v("pre-1"), v("commented"), v("bob"), null, null, v("same words"), v("2026-07-01 11:00:00")): 2,
		d(v("pre-1"), v("status_changed"), v("alice"), null, v(""), null, v("2026-07-01 12:00:00")):    1,
		d(v("pre-2"), v("updated"), v("carol"), v(""), null, v(""), v("2026-07-02 09:00:00")):          1,
	}
}

// liveEvents reads the live events table as digest -> ids.
func liveEvents(t *testing.T, ctx context.Context, conn *sql.Conn) map[string][]string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT id, "+preflipEventDigestColumns+" FROM events")
	if err != nil {
		t.Fatalf("read live events: %v", err)
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var id string
		fields := make([]sql.NullString, 7)
		dests := []any{&id}
		for i := range fields {
			dests = append(dests, &fields[i])
		}
		if err := rows.Scan(dests...); err != nil {
			t.Fatalf("scan live events: %v", err)
		}
		digest := rowid.Digest(fields)
		out[digest] = append(out[digest], id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read live events: %v", err)
	}
	for _, ids := range out {
		sort.Strings(ids)
	}
	return out
}

// requireContent fails unless the live table holds exactly want's
// multiplicities.
func requireContent(t *testing.T, got map[string][]string, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("live events hold %d distinct contents, want %d: %v", len(got), len(want), got)
	}
	for digest, n := range want {
		if len(got[digest]) != n {
			t.Fatalf("content %s has %d live rows %v, want %d", digest[:12], len(got[digest]), got[digest], n)
		}
	}
}

// requireDerivedIDs fails unless every row of every content holds one of that
// content's derived ids for ordinals 0..n-1.
func requireDerivedIDs(t *testing.T, got map[string][]string) {
	t.Helper()
	for digest, ids := range got {
		want := make([]string, len(ids))
		for i := range ids {
			want[i] = rowid.New("events", i, digest)
		}
		sort.Strings(want)
		for i := range ids {
			if ids[i] != want[i] {
				t.Fatalf("content %s ids = %v, want the derived ids %v", digest[:12], ids, want)
			}
		}
	}
}

func ignoredCursorHas(t *testing.T, ctx context.Context, conn *sql.Conn, version int) bool {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = ?", version) == 1
}

func eventsTablePresent(t *testing.T, ctx context.Context, conn *sql.Conn) bool {
	t.Helper()
	return scalarInt(t, ctx, conn,
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'events'") == 1
}

// publishPreflipLineage seeds pre-flip events on a database at main 61 and
// pushes it to a fresh file:// remote. It returns the origin's data dir and
// the remote URL.
func publishPreflipLineage(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	originData := seedMainSchemaAt(t, ctx, 61)
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir
	withRawConn(t, originData, preflipDatabase, func(conn *sql.Conn) {
		seedPreflipEvents(t, ctx, conn)
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	return originData, remoteURL
}

// cloneLineage clones the remote into a new beads dir and returns it with its
// data dir.
func cloneLineage(t *testing.T, ctx context.Context, remoteURL string) (string, string) {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create clone data dir: %v", err)
	}
	withRawConn(t, dataDir, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, preflipDatabase)
	})
	return beadsDir, dataDir
}

// migrateAndPublishOrigin runs the full migration on the origin (the clone
// that runs 0062 itself) and pushes the result.
func migrateAndPublishOrigin(t *testing.T, ctx context.Context, originData string) {
	t.Helper()
	withRawConn(t, originData, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (migrator): %v", err)
		}
		requireContent(t, liveEvents(t, ctx, conn), preflipSeededContent())
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
}

// requireRestoredThroughStore opens the clone the way bd does (the open
// migrates) and reads the history back through the production reader, then
// checks the table itself.
func requireRestoredThroughStore(t *testing.T, ctx context.Context, beadsDir, dataDir string) {
	t.Helper()
	store, err := embeddeddolt.Open(ctx, beadsDir, preflipDatabase, "main")
	if err != nil {
		t.Fatalf("open clone: %v", err)
	}
	for issue, want := range map[string]int{"pre-1": 4, "pre-2": 1} {
		events, err := store.GetEvents(ctx, issue, 0)
		if err != nil {
			closeEmbeddedDoltStore(t, store)
			t.Fatalf("GetEvents(%s): %v", issue, err)
		}
		if len(events) != want {
			closeEmbeddedDoltStore(t, store)
			t.Fatalf("GetEvents(%s) returned %d events after the open, want the %d pre-upgrade ones", issue, len(events), want)
		}
	}
	closeEmbeddedDoltStore(t, store)

	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		got := liveEvents(t, ctx, conn)
		requireContent(t, got, preflipSeededContent())
		requireDerivedIDs(t, got)
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after the restore")
		}
		assertEventsAbsentFromStatus(t, ctx, conn)
	})
}

// TestEmbeddedIgnoredMigration0031RestoresPreflipEvents is the reported bug,
// end to end over a file:// remote: a clone taken before the flip adopts 0062
// by pull, and a clone taken after it starts fresh. Both must come out of
// their first open with the pre-upgrade rows, under content-derived ids.
func TestEmbeddedIgnoredMigration0031RestoresPreflipEvents(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	originData, remoteURL := publishPreflipLineage(t, ctx)
	adopterBeads, adopterData := cloneLineage(t, ctx, remoteURL)
	migrateAndPublishOrigin(t, ctx, originData)

	t.Run("adopt by pull", func(t *testing.T) {
		withRawConn(t, adopterData, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, "CALL DOLT_PULL('origin', 'main')")
			if eventsTablePresent(t, ctx, conn) {
				t.Fatal("events survived the pull of 0062's committed drop; the adopter shape is not reproduced")
			}
		})
		requireRestoredThroughStore(t, ctx, adopterBeads, adopterData)
	})

	t.Run("fresh clone after the flip", func(t *testing.T) {
		freshBeads, freshData := cloneLineage(t, ctx, remoteURL)
		requireRestoredThroughStore(t, ctx, freshBeads, freshData)
	})
}

// TestEmbeddedIgnoredMigration0031RestoresAfterLostTable is the triage repro's
// shape on one database: the migrator's committed history carries the
// pre-flip rows, and a working set that lost events gets them back through the
// events sentinel replay (ignored/0019, then 0031), by content.
func TestEmbeddedIgnoredMigration0031RestoresAfterLostTable(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedPreflipEvents(t, ctx, conn)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}
	execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS events")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (lost table): %v", err)
	}
	got := liveEvents(t, ctx, conn)
	requireContent(t, got, preflipSeededContent())
	requireDerivedIDs(t, got)
	assertEventsAbsentFromStatus(t, ctx, conn)
}
