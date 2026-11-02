//go:build cgo

package embeddeddolt_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
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

// TestEmbeddedIgnoredMigration0031KeepsExistingRows pins the reconcile: rows
// already live keep their ids whatever those ids are (legacy, or an id that
// is neither legacy nor derived), a row with no pre-upgrade counterpart is
// left alone, and only the missing copies of a content are added, under the
// lowest derived ids no live row holds.
func TestEmbeddedIgnoredMigration0031KeepsExistingRows(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedPreflipEvents(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}

	// Lose the unique 'created' row and one copy of the duplicate pair,
	// re-id a surviving row, and record a post-flip event.
	execFrozenGuard(t, ctx, conn, `
DELETE FROM events WHERE id IN ('legacy-1', 'legacy-3');
UPDATE events SET id = 'kept-custom-id' WHERE id = 'legacy-4';
INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES
  ('pre-2', 'closed', 'dave', NULL, NULL, NULL, '2026-08-01 08:00:00', 'evt-post-1');
`)
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 31")
	before := liveEvents(t, ctx, conn)
	if n := len(before); n != 4 {
		t.Fatalf("setup left %d distinct contents, want 4: %v", n, before)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (restore): %v", err)
	}
	after := liveEvents(t, ctx, conn)

	want := preflipSeededContent()
	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	null := sql.NullString{}
	postFlip := rowid.Digest([]sql.NullString{v("pre-2"), v("closed"), v("dave"), null, null, null, v("2026-08-01 08:00:00")})
	want[postFlip] = 1
	requireContent(t, after, want)

	for digest, ids := range before {
		for _, id := range ids {
			if !slices.Contains(after[digest], id) {
				t.Fatalf("live row %s (content %s) did not survive the restore; after = %v", id, digest[:12], after[digest])
			}
		}
	}
	created := rowid.Digest([]sql.NullString{v("pre-1"), v("created"), v("alice"), null, null, null, v("2026-07-01 10:00:00")})
	if got, wantID := after[created], rowid.New("events", 0, created); len(got) != 1 || got[0] != wantID {
		t.Fatalf("restored 'created' row ids = %v, want [%s]", got, wantID)
	}
	dup := rowid.Digest([]sql.NullString{v("pre-1"), v("commented"), v("bob"), null, null, v("same words"), v("2026-07-01 11:00:00")})
	if got, wantIDs := after[dup], []string{"legacy-2", rowid.New("events", 0, dup)}; !sameStrings(got, wantIDs) {
		t.Fatalf("duplicate pair ids = %v, want the survivor plus one derived copy %v", got, wantIDs)
	}
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after the restore")
	}
}

func sameStrings(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// TestEmbeddedIgnoredMigration0031SkipsDeletedIssues pins that a pre-upgrade
// row whose issue has since been deleted is skipped -- fk_events_issue would
// refuse it -- and reported, while the rest of the history comes back.
func TestEmbeddedIgnoredMigration0031SkipsDeletedIssues(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedPreflipEvents(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}
	execFrozenGuard(t, ctx, conn, "DELETE FROM issues WHERE id = 'pre-2'")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: delete pre-2')")
	execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS events")

	var notice bytes.Buffer
	defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (lost table): %v", err)
	}

	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	want := preflipSeededContent()
	delete(want, rowid.Digest([]sql.NullString{v("pre-2"), v("updated"), v("carol"), v(""), {}, v(""), v("2026-07-02 09:00:00")}))
	got := liveEvents(t, ctx, conn)
	requireContent(t, got, want)
	requireDerivedIDs(t, got)
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after the restore")
	}
	for _, line := range []string{
		"Restored 4 pre-upgrade audit event(s)",
		"Skipped 1 pre-upgrade audit event(s) whose issue id no longer exists (deleted or renamed)",
	} {
		if !strings.Contains(notice.String(), line) {
			t.Fatalf("restore notice = %q, want it to report %q", notice.String(), line)
		}
	}
}

// eventsFlipDropMessage is the message main migration 0062 commits its drop
// under (pinned to the frozen 0062 bytes in the schema package).
const eventsFlipDropMessage = "schema: drop versioned events table for ignored-plane flip (bd-red8u)"

// TestEmbeddedIgnoredMigration0031WithoutSourceIsANoOp pins the clones with
// nothing to restore: one whose history never carried versioned events (no
// drop commit), and one whose drop commit's first parent holds no events
// table. Both record 0031, keep the live table as it was, and print nothing.
func TestEmbeddedIgnoredMigration0031WithoutSourceIsANoOp(t *testing.T) {
	requireEmbedded(t)

	for name, addDrop := range map[string]bool{"no drop commit": false, "parent without events": true} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			defer closeConn()
			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				t.Fatalf("MigrateUp (fresh): %v", err)
			}
			execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('post-1', 'after the flip', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('post-1', 'created', 'erin', '2026-09-01 10:00:00', 'evt-post-1');
`)
			mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: post-flip issue')")
			if addDrop {
				mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--allow-empty', '-m', ?)", eventsFlipDropMessage)
			}
			mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 31")
			before := liveEvents(t, ctx, conn)

			var notice bytes.Buffer
			defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				t.Fatalf("MigrateUp: %v", err)
			}
			if after := liveEvents(t, ctx, conn); len(before) != 1 || !reflect.DeepEqual(after, before) {
				t.Fatalf("live events changed with nothing to restore: before %v, after %v", before, after)
			}
			if !ignoredCursorHas(t, ctx, conn, 31) {
				t.Fatal("ignored cursor does not record 0031 with nothing to restore")
			}
			if notice.Len() != 0 {
				t.Fatalf("restore printed %q with nothing to restore", notice.String())
			}
		})
	}
}

// TestEmbeddedIgnoredMigration0031RetryAfterPartialRestore pins the failure
// contract: a live write that fails after some restored rows have landed
// fails the pass with 0031 still pending, and the next pass completes the
// restore without adding any row twice.
func TestEmbeddedIgnoredMigration0031RetryAfterPartialRestore(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedPreflipEvents(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}
	// The adopter's state: events present and empty, 0031 not yet recorded.
	execFrozenGuard(t, ctx, conn, "DELETE FROM events")
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 31")

	// One issue per page and one row per INSERT, failing after the second.
	restoreSizes := schema.SetPreflipEventsBatchSizesForTest(1, 1)
	defer restoreSizes()
	injected := errors.New("injected live write failure")
	batches := 0
	restoreHook := schema.SetPreflipEventsInsertFaultHookForTest(func() error {
		if batches++; batches == 2 {
			return injected
		}
		return nil
	})
	_, err := schema.MigrateUp(ctx, conn)
	restoreHook()
	if !errors.Is(err, injected) {
		t.Fatalf("MigrateUp with a failing restore write = %v, want the injected failure", err)
	}
	if ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor recorded 0031 after the restore failed; the rest of the history would never come back")
	}
	partial := 0
	for _, ids := range liveEvents(t, ctx, conn) {
		partial += len(ids)
	}
	if partial != 2 {
		t.Fatalf("live events hold %d rows after the failure, want the 2 written before it", partial)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (retry): %v", err)
	}
	got := liveEvents(t, ctx, conn)
	requireContent(t, got, preflipSeededContent())
	requireDerivedIDs(t, got)
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after the retry")
	}
}

// TestEmbeddedIgnoredMigration0031LeavesHistoryAndWorkingSetAlone pins what an
// open with only 0031 pending may touch: nothing but the ignored events
// table. It needs no migration consent, makes no commit, keeps an unrelated
// uncommitted edit as it was, writes no journal entry, and leaves events
// dolt-ignored.
func TestEmbeddedIgnoredMigration0031LeavesHistoryAndWorkingSetAlone(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	beadsDir := filepath.Dir(dataDir)
	var head string
	var journal int
	var dirty []string
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		seedPreflipEvents(t, ctx, conn)
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (migrator): %v", err)
		}
		// The adopter's state, plus an operator's uncommitted edit.
		execFrozenGuard(t, ctx, conn, `
DELETE FROM events;
DELETE FROM ignored_schema_migrations WHERE version = 31;
UPDATE issues SET title = 'edited, not committed' WHERE id = 'pre-1';
`)
		head = scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
		journal = scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM bd_events_journal")
		var err error
		if dirty, err = statusTables(ctx, conn); err != nil {
			t.Fatalf("read dolt_status: %v", err)
		}
	})

	schema.SetLocalMigrateConsent(false)
	schema.SetForceAllowRemoteMigrate(false)
	t.Cleanup(func() { schema.SetLocalMigrateConsent(true) })
	t.Setenv(schema.AllowMigrateEnv, "")
	t.Setenv(schema.AllowRemoteMigrateEnv, "")
	store, err := embeddeddolt.Open(ctx, beadsDir, preflipDatabase, "main")
	if err != nil {
		t.Fatalf("open with only ignored 0031 pending and no consent: %v", err)
	}
	closeEmbeddedDoltStore(t, store)

	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		requireContent(t, liveEvents(t, ctx, conn), preflipSeededContent())
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after the open")
		}
		if got := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')"); got != head {
			t.Fatalf("HEAD moved from %s to %s; the restore must not commit", head, got)
		}
		if got := scalarString(t, ctx, conn, "SELECT title FROM issues WHERE id = 'pre-1'"); got != "edited, not committed" {
			t.Fatalf("working-set title = %q, want the operator's uncommitted edit kept", got)
		}
		if got := scalarString(t, ctx, conn, "SELECT title FROM issues AS OF 'HEAD' WHERE id = 'pre-1'"); got != "first" {
			t.Fatalf("HEAD title = %q, want the committed one", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM bd_events_journal"); got != journal {
			t.Fatalf("bd_events_journal holds %d rows after the open, want the %d it had", got, journal)
		}
		tables, err := statusTables(ctx, conn)
		if err != nil {
			t.Fatalf("read dolt_status: %v", err)
		}
		if !sameStrings(tables, dirty) {
			t.Fatalf("dolt_status = %v after the open, want it as the open found it: %v", tables, dirty)
		}
		assertEventsAbsentFromStatus(t, ctx, conn)
	})
}

// twoSourceLineage is a migrator's database whose history carries two
// commits under 0062's drop message: an empty one made by hand over a first
// source holding pre-1's row, then 0062's own over a second source holding
// pre-1's and pre-2's rows (second is that commit's first parent: MigrateUp
// may commit between the seed and 0062). It is left in the adopter's state (events empty,
// 0031 pending). With dropComment, the first source's events table lacks the
// comment column.
type twoSourceLineage struct {
	conn          *sql.Conn
	first, second string
	pre1, pre2    string // content digests
}

func newTwoSourceLineage(t *testing.T, ctx context.Context, dropComment bool) *twoSourceLineage {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	t.Cleanup(closeConn)

	if dropComment {
		execFrozenGuard(t, ctx, conn, "ALTER TABLE events DROP COLUMN comment")
	}
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('pre-1', 'first', '', '', '', '', 'open', 2, 'task'),
  ('pre-2', 'second', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-1', 'created', 'alice', '2026-07-01 10:00:00', 'legacy-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: first pre-flip source')")
	l := &twoSourceLineage{conn: conn, first: scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")}
	mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--allow-empty', '-m', ?)", eventsFlipDropMessage)
	handDrop := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
	if dropComment {
		execFrozenGuard(t, ctx, conn, "ALTER TABLE events ADD COLUMN comment TEXT")
	}
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-2', 'updated', 'carol', '2026-07-02 09:00:00', 'legacy-5');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: second pre-flip source')")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}
	if err := conn.QueryRowContext(ctx, `
SELECT a.parent_hash FROM dolt_commit_ancestors a JOIN dolt_log l ON l.commit_hash = a.commit_hash
WHERE l.message = ? AND l.commit_hash <> ? AND a.parent_index = 0`,
		eventsFlipDropMessage, handDrop).Scan(&l.second); err != nil {
		t.Fatalf("find 0062's drop commit parent: %v", err)
	}
	execFrozenGuard(t, ctx, conn, "DELETE FROM events")
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 31")

	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	null := sql.NullString{}
	l.pre1 = rowid.Digest([]sql.NullString{v("pre-1"), v("created"), v("alice"), null, null, null, v("2026-07-01 10:00:00")})
	l.pre2 = rowid.Digest([]sql.NullString{v("pre-2"), v("updated"), v("carol"), null, null, null, v("2026-07-02 09:00:00")})
	return l
}

// TestEmbeddedIgnoredMigration0031SkipsAnUnreadableSource pins the per-source
// degradation: a source whose history cannot be read (Dolt's schema-encoding
// drift) or whose events table lacks a column the restore reads is skipped
// with one warning naming it and the upgrading guide, the other source is
// still restored, 0031 is recorded, and neither that open nor the next fails.
// A drift-shaped failure of a LIVE write is not a source problem: it fails the
// pass with 0031 pending.
func TestEmbeddedIgnoredMigration0031SkipsAnUnreadableSource(t *testing.T) {
	requireEmbedded(t)

	requireSkipped := func(t *testing.T, ctx context.Context, l *twoSourceLineage, skipped string, want map[string]int) {
		t.Helper()
		var notice bytes.Buffer
		defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
		if _, err := schema.MigrateUp(ctx, l.conn); err != nil {
			t.Fatalf("MigrateUp with an unreadable source: %v", err)
		}
		got := liveEvents(t, ctx, l.conn)
		requireContent(t, got, want)
		requireDerivedIDs(t, got)
		if !ignoredCursorHas(t, ctx, l.conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after skipping the unreadable source")
		}
		warning := notice.String()
		for _, part := range []string{"Warning", skipped, "https://beads.gascity.com/getting-started/upgrading"} {
			if !strings.Contains(warning, part) {
				t.Fatalf("restore notice = %q, want a warning carrying %q", warning, part)
			}
		}
		if _, err := schema.MigrateUp(ctx, l.conn); err != nil {
			t.Fatalf("second MigrateUp after skipping the unreadable source: %v", err)
		}
	}

	t.Run("drift reading the history", func(t *testing.T) {
		ctx := t.Context()
		l := newTwoSourceLineage(t, ctx, false)
		defer schema.SetPreflipEventsSourceFaultHookForTest(func(ref, _ string) error {
			if ref == l.second {
				return errors.New("panic: invalid hash length: 19")
			}
			return nil
		})()
		requireSkipped(t, ctx, l, l.second, map[string]int{l.pre1: 1})
	})

	t.Run("source lacks a column", func(t *testing.T) {
		ctx := t.Context()
		l := newTwoSourceLineage(t, ctx, true)
		requireSkipped(t, ctx, l, l.first, map[string]int{l.pre1: 1, l.pre2: 1})
	})

	t.Run("drift on a live write fails the pass", func(t *testing.T) {
		ctx := t.Context()
		l := newTwoSourceLineage(t, ctx, false)
		restoreHook := schema.SetPreflipEventsInsertFaultHookForTest(func() error {
			return errors.New("panic: invalid hash length: 19")
		})
		_, err := schema.MigrateUp(ctx, l.conn)
		restoreHook()
		if err == nil || !strings.Contains(err.Error(), "invalid hash length") {
			t.Fatalf("MigrateUp with a drifting live write = %v, want that failure", err)
		}
		if ignoredCursorHas(t, ctx, l.conn, 31) {
			t.Fatal("ignored cursor recorded 0031 after a live write failed")
		}
	})
}

// TestEmbeddedIgnoredMigration0031MergesComplementarySources pins the
// reconcile across two sources that each hold rows the other lacks: every
// content comes back at the largest multiplicity any one source holds, never
// the sum, so a content both sources carry is not restored twice.
func TestEmbeddedIgnoredMigration0031MergesComplementarySources(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// First source: a only there, and two copies of shared.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('pre-1', 'first', '', '', '', '', 'open', 2, 'task'),
  ('pre-2', 'second', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-1', 'created', 'alice', '2026-07-01 10:00:00', 'legacy-a'),
  ('pre-1', 'commented', 'bob', '2026-07-01 11:00:00', 'legacy-s1'),
  ('pre-1', 'commented', 'bob', '2026-07-01 11:00:00', 'legacy-s2');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: first pre-flip source')")
	mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--allow-empty', '-m', ?)", eventsFlipDropMessage)

	// Second source: one copy of shared, b only there, and three copies of c.
	execFrozenGuard(t, ctx, conn, `
DELETE FROM events WHERE id IN ('legacy-a', 'legacy-s2');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-2', 'updated', 'carol', '2026-07-02 09:00:00', 'legacy-b'),
  ('pre-2', 'labeled', 'dave', '2026-07-02 10:00:00', 'legacy-c1'),
  ('pre-2', 'labeled', 'dave', '2026-07-02 10:00:00', 'legacy-c2'),
  ('pre-2', 'labeled', 'dave', '2026-07-02 10:00:00', 'legacy-c3');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: second pre-flip source')")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}
	execFrozenGuard(t, ctx, conn, "DELETE FROM events")
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 31")

	var notice bytes.Buffer
	defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (restore): %v", err)
	}

	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	null := sql.NullString{}
	d := func(issue, typ, actor, at string) string {
		return rowid.Digest([]sql.NullString{v(issue), v(typ), v(actor), null, null, null, v(at)})
	}
	got := liveEvents(t, ctx, conn)
	requireContent(t, got, map[string]int{
		d("pre-1", "created", "alice", "2026-07-01 10:00:00"): 1,
		d("pre-1", "commented", "bob", "2026-07-01 11:00:00"): 2,
		d("pre-2", "updated", "carol", "2026-07-02 09:00:00"): 1,
		d("pre-2", "labeled", "dave", "2026-07-02 10:00:00"):  3,
	})
	requireDerivedIDs(t, got)
	if !strings.Contains(notice.String(), "Restored 7 pre-upgrade audit event(s)") {
		t.Fatalf("restore notice = %q, want it to count the 7 restored rows", notice.String())
	}
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after the restore")
	}
}

// preflipNullDigest is the content digest of an events row whose old_value,
// new_value and comment are NULL.
func preflipNullDigest(issue, eventType, actor, createdAt string) string {
	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	null := sql.NullString{}
	return rowid.Digest([]sql.NullString{v(issue), v(eventType), v(actor), null, null, null, v(createdAt)})
}

// adoptPreflipLineage runs the migrator's pass on conn and leaves the
// adopter's state: events present and empty, 0031 pending.
func adoptPreflipLineage(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}
	execFrozenGuard(t, ctx, conn, "DELETE FROM events")
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 31")
}

// TestEmbeddedIgnoredMigration0031SkipsADerivedIDAnotherIssueHolds pins the
// table-wide id probe: a live row of another issue that holds the first
// derived id of a restored content (an issue rename re-points issue_id, never
// the id) keeps it untouched, and the restored copy takes the next ordinal.
func TestEmbeddedIgnoredMigration0031SkipsADerivedIDAnotherIssueHolds(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedPreflipEvents(t, ctx, conn)
	adoptPreflipLineage(t, ctx, conn)

	created := preflipNullDigest("pre-1", "created", "alice", "2026-07-01 10:00:00")
	taken := rowid.New("events", 0, created)
	mustExecOn(t, ctx, conn, `INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('pre-2', 'renamed', 'erin', '2026-08-01 08:00:00', ?)`, taken)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (restore): %v", err)
	}
	got := liveEvents(t, ctx, conn)
	want := preflipSeededContent()
	renamed := preflipNullDigest("pre-2", "renamed", "erin", "2026-08-01 08:00:00")
	want[renamed] = 1
	requireContent(t, got, want)
	if ids, wantID := got[created], rowid.New("events", 1, created); len(ids) != 1 || ids[0] != wantID {
		t.Fatalf("restored 'created' row ids = %v, want [%s] (ordinal 0 is another issue's)", ids, wantID)
	}
	if ids := got[renamed]; len(ids) != 1 || ids[0] != taken {
		t.Fatalf("other issue's row ids = %v, want it to keep %s", ids, taken)
	}
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after the restore")
	}
}

// TestEmbeddedIgnoredMigration0031CountsEveryOrphan pins the orphan count:
// every skipped row of a deleted issue is counted, whether the issue leads a
// page, fills the whole source, or appears in more than one source (each
// source's rows count), the count prints even when nothing was restored, and
// fk_events_issue still refuses an orphan afterwards.
func TestEmbeddedIgnoredMigration0031CountsEveryOrphan(t *testing.T) {
	requireEmbedded(t)

	// gone-1 sorts before live-1, so with one issue per page the first page
	// holds nothing but orphans.
	orphanLineage := func(t *testing.T, ctx context.Context, deleted string) *sql.Conn {
		t.Helper()
		dataDir := seedMainSchemaAt(t, ctx, 61)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		t.Cleanup(closeConn)
		execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('gone-1', 'deleted after the flip', '', '', '', '', 'open', 2, 'task'),
  ('live-1', 'kept', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('gone-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-gone-1'),
  ('gone-1', 'updated', 'tester', '2026-07-30 00:01:00', 'evt-gone-2'),
  ('live-1', 'created', 'tester', '2026-07-30 00:02:00', 'evt-live-1');
`)
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: seed pre-flip events')")
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (migrator): %v", err)
		}
		execFrozenGuard(t, ctx, conn, "DELETE FROM issues WHERE id IN ("+deleted+")")
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: delete issues')")
		execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS events")
		return conn
	}
	restore := func(t *testing.T, ctx context.Context, conn *sql.Conn) string {
		t.Helper()
		var notice bytes.Buffer
		defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (restore): %v", err)
		}
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after the restore")
		}
		return notice.String()
	}
	requireFKEnforced := func(t *testing.T, ctx context.Context, conn *sql.Conn, issue string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, `INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES (?, 'created', 'tester', '2026-09-01 00:00:00', 'evt-orphan-probe')`, issue); err == nil {
			t.Fatalf("inserting an event of deleted issue %s succeeded; the restore left foreign keys off", issue)
		}
	}

	t.Run("leading orphan page", func(t *testing.T) {
		ctx := t.Context()
		defer schema.SetPreflipEventsBatchSizesForTest(1, 1)()
		conn := orphanLineage(t, ctx, "'gone-1'")
		notice := restore(t, ctx, conn)
		got := liveEvents(t, ctx, conn)
		requireContent(t, got, map[string]int{preflipNullDigest("live-1", "created", "tester", "2026-07-30 00:02:00"): 1})
		requireDerivedIDs(t, got)
		for _, line := range []string{
			"Restored 1 pre-upgrade audit event(s)",
			"Skipped 2 pre-upgrade audit event(s) whose issue id no longer exists (deleted or renamed)",
		} {
			if !strings.Contains(notice, line) {
				t.Fatalf("restore notice = %q, want it to report %q", notice, line)
			}
		}
		requireFKEnforced(t, ctx, conn, "gone-1")
	})

	t.Run("only orphans", func(t *testing.T) {
		ctx := t.Context()
		conn := orphanLineage(t, ctx, "'gone-1', 'live-1'")
		notice := restore(t, ctx, conn)
		if n := len(liveEvents(t, ctx, conn)); n != 0 {
			t.Fatalf("live events hold %d contents, want none: every issue is gone", n)
		}
		if strings.Contains(notice, "Restored") || !strings.Contains(notice, "Skipped 3 pre-upgrade audit event(s) whose issue id no longer exists (deleted or renamed)") {
			t.Fatalf("restore notice = %q, want only the count of the 3 skipped rows", notice)
		}
		requireFKEnforced(t, ctx, conn, "live-1")
	})

	t.Run("orphan in two sources", func(t *testing.T) {
		ctx := t.Context()
		l := newTwoSourceLineage(t, ctx, false)
		execFrozenGuard(t, ctx, l.conn, "DELETE FROM issues WHERE id = 'pre-1'")
		notice := restore(t, ctx, l.conn)
		requireContent(t, liveEvents(t, ctx, l.conn), map[string]int{l.pre2: 1})
		for _, line := range []string{
			"Restored 1 pre-upgrade audit event(s)",
			"Skipped 2 pre-upgrade audit event(s) whose issue id no longer exists (deleted or renamed)",
		} {
			if !strings.Contains(notice, line) {
				t.Fatalf("restore notice = %q, want it to report %q", notice, line)
			}
		}
		requireFKEnforced(t, ctx, l.conn, "pre-1")
	})
}

// TestEmbeddedIgnoredMigration0031ResumesAnInterruptedRestore pins the retry
// contract on the states an interrupted pass leaves: rows an earlier attempt
// wrote under derived ids are reused, never duplicated; a failure after
// a full batch landed, a failing cursor write after the restore, and a
// transient failure reading history all fail the pass with 0031 pending, and
// the next pass converges on exact multiplicities.
func TestEmbeddedIgnoredMigration0031ResumesAnInterruptedRestore(t *testing.T) {
	requireEmbedded(t)

	t.Run("rows an earlier attempt wrote are reused", func(t *testing.T) {
		ctx := t.Context()
		dataDir := seedMainSchemaAt(t, ctx, 61)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()
		execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('r-1', 'reached', '', '', '', '', 'open', 2, 'task'),
  ('r-2', 'unreached', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('r-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-r-1a'),
  ('r-2', 'status_changed', 'tester', '2026-07-30 00:01:00', 'evt-r-2a'),
  ('r-2', 'status_changed', 'tester', '2026-07-30 00:01:00', 'evt-r-2b');
`)
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: seed pre-flip events')")
		adoptPreflipLineage(t, ctx, conn)
		r1 := preflipNullDigest("r-1", "created", "tester", "2026-07-30 00:00:00")
		mustExecOn(t, ctx, conn, `INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('r-1', 'created', 'tester', '2026-07-30 00:00:00', ?)`, rowid.New("events", 0, r1))

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (retry): %v", err)
		}
		got := liveEvents(t, ctx, conn)
		requireContent(t, got, map[string]int{r1: 1, preflipNullDigest("r-2", "status_changed", "tester", "2026-07-30 00:01:00"): 2})
		requireDerivedIDs(t, got)
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after the retry")
		}
	})

	t.Run("pass fails after a full batch landed", func(t *testing.T) {
		ctx := t.Context()
		dataDir := seedMainSchemaAt(t, ctx, 61)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()
		var bulk strings.Builder
		bulk.WriteString(`
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('f-1', 'busy', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, comment, created_at, id) VALUES
`)
		for i := range 102 {
			if i > 0 {
				bulk.WriteByte(',')
			}
			fmt.Fprintf(&bulk, "('f-1', 'commented', 'tester', 'note %03d', '2026-07-30 00:%02d:%02d', 'evt-f-%03d')\n", i, i/60, i%60, i)
		}
		bulk.WriteString(";\n")
		execFrozenGuard(t, ctx, conn, bulk.String())
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: seed pre-flip events')")
		adoptPreflipLineage(t, ctx, conn)

		defer schema.SetPreflipEventsBatchSizesForTest(100, 100)()
		injected := errors.New("injected: connection reset mid-restore")
		// The hook runs once a batch is written: fail after the first 100.
		batches := 0
		restoreHook := schema.SetPreflipEventsInsertFaultHookForTest(func() error {
			if batches++; batches == 1 {
				return injected
			}
			return nil
		})
		_, err := schema.MigrateUp(ctx, conn)
		restoreHook()
		if !errors.Is(err, injected) {
			t.Fatalf("MigrateUp failing after the first batch = %v, want the injected failure", err)
		}
		if ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor recorded 0031 after the restore failed")
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 100 {
			t.Fatalf("live events hold %d rows after the failure, want the first batch's 100", got)
		}
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (retry): %v", err)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(DISTINCT id) FROM events"); got != 102 {
			t.Fatalf("live events hold %d distinct ids after the retry, want 102", got)
		}
		requireDerivedIDs(t, liveEvents(t, ctx, conn))
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after the retry")
		}
	})

	t.Run("cursor write fails after the restore", func(t *testing.T) {
		ctx := t.Context()
		dataDir := seedMainSchemaAt(t, ctx, 61)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()
		seedPreflipEvents(t, ctx, conn)
		adoptPreflipLineage(t, ctx, conn)

		// Hide the cursor table once the first restored batch lands, so the
		// restore completes and recording 0031 is what fails.
		hidden := false
		restoreHook := schema.SetPreflipEventsInsertFaultHookForTest(func() error {
			if !hidden {
				hidden = true
				_, err := conn.ExecContext(ctx, "RENAME TABLE ignored_schema_migrations TO ignored_schema_migrations_hidden")
				return err
			}
			return nil
		})
		_, err := schema.MigrateUp(ctx, conn)
		restoreHook()
		if !hidden {
			t.Fatal("the restore wrote no batch; the fixture never hid the cursor table")
		}
		if err == nil {
			t.Fatal("MigrateUp with the cursor table gone succeeded; 0031 cannot have been recorded")
		}
		mustExecOn(t, ctx, conn, "RENAME TABLE ignored_schema_migrations_hidden TO ignored_schema_migrations")
		if ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor records 0031 though writing it failed")
		}
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (retry): %v", err)
		}
		got := liveEvents(t, ctx, conn)
		requireContent(t, got, preflipSeededContent())
		requireDerivedIDs(t, got)
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after the retry")
		}
	})

	t.Run("transient history read fails", func(t *testing.T) {
		ctx := t.Context()
		dataDir := seedMainSchemaAt(t, ctx, 61)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()
		seedPreflipEvents(t, ctx, conn)
		adoptPreflipLineage(t, ctx, conn)

		transient := errors.New("driver: bad connection")
		var notice bytes.Buffer
		defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
		restoreHook := schema.SetPreflipEventsSourceFaultHookForTest(func(_, issue string) error {
			if issue == "pre-2" {
				return transient
			}
			return nil
		})
		_, err := schema.MigrateUp(ctx, conn)
		restoreHook()
		if !errors.Is(err, transient) {
			t.Fatalf("MigrateUp with a transient history failure = %v, want that failure", err)
		}
		if ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor recorded 0031 after a transient failure; the source would never be read again")
		}
		if strings.Contains(notice.String(), "Warning") {
			t.Fatalf("restore notice = %q; a transient failure must not be reported as an unreadable source", notice.String())
		}
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (retry): %v", err)
		}
		got := liveEvents(t, ctx, conn)
		requireContent(t, got, preflipSeededContent())
		requireDerivedIDs(t, got)
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("ignored cursor does not record 0031 after the retry")
		}
	})
}

// TestEmbeddedIgnoredMigration0031SkipsARetrackedSource pins the flap shape:
// events was tracked again after the flip, without its comment column, and
// dropped again under 0062's frozen message, so history holds two drop
// commits. The retracked source is skipped with the warning, the real one is
// restored, and the next pass opens cleanly.
func TestEmbeddedIgnoredMigration0031SkipsARetrackedSource(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('s-1', 'survivor', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('s-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-s-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: seed pre-flip events')")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (migrator): %v", err)
	}

	execFrozenGuard(t, ctx, conn, `
DROP TABLE IF EXISTS events;
CREATE TABLE events (
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value LONGTEXT,
    new_value LONGTEXT,
    created_at DATETIME NOT NULL,
    id CHAR(36) NOT NULL,
    PRIMARY KEY (id)
);
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('s-1', 'flapped', 'tester', '2026-07-31 00:00:00', 'evt-flap-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-f', 'events')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: retrack events without comment')")
	retracked := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
	execFrozenGuard(t, ctx, conn, "DROP TABLE events")
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-f', 'events')")
	mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', ?)", eventsFlipDropMessage)
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", eventsFlipDropMessage); got != 2 {
		t.Fatalf("fixture holds %d drop commits, want 2", got)
	}
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 31")

	var notice bytes.Buffer
	defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp with a retracked source: %v", err)
	}
	got := liveEvents(t, ctx, conn)
	requireContent(t, got, map[string]int{preflipNullDigest("s-1", "created", "tester", "2026-07-30 00:00:00"): 1})
	requireDerivedIDs(t, got)
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after skipping the retracked source")
	}
	for _, part := range []string{"Warning", retracked, "incomplete", "https://beads.gascity.com/getting-started/upgrading"} {
		if !strings.Contains(notice.String(), part) {
			t.Fatalf("restore notice = %q, want a warning carrying %q", notice.String(), part)
		}
	}
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (reopen): %v", err)
	}
	assertEventsAbsentFromStatus(t, ctx, conn)
}

// TestEmbeddedIgnoredMigration0031NeverSkipsACanceledRead pins that a
// cancellation wins over the error's shape: a history read cut short by a
// canceled open that surfaces as drift fails the pass with 0031 pending
// instead of skipping the source for good.
func TestEmbeddedIgnoredMigration0031NeverSkipsACanceledRead(t *testing.T) {
	requireEmbedded(t)
	l := newTwoSourceLineage(t, t.Context(), false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var notice bytes.Buffer
	defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
	restoreHook := schema.SetPreflipEventsSourceFaultHookForTest(func(string, string) error {
		cancel()
		return errors.New("panic: invalid hash length: 19")
	})
	_, err := schema.MigrateUp(ctx, l.conn)
	restoreHook()
	if err == nil {
		t.Fatal("MigrateUp canceled mid-read succeeded; the canceled source was skipped")
	}
	if strings.Contains(notice.String(), "Warning") {
		t.Fatalf("restore notice = %q; a canceled read must not be reported as an unreadable source", notice.String())
	}
	if ignoredCursorHas(t, t.Context(), l.conn, 31) {
		t.Fatal("ignored cursor recorded 0031 after a canceled pass")
	}
}

// TestEmbeddedIgnoredMigration0031SkipsASourceFailingMidway pins a source
// whose history fails partway: the issues it restored before the failure
// stay, the issue it was reading gets nothing from it, the other source still
// restores, the warning names the failing source, and later writable and
// read-only opens neither read history again nor print anything.
func TestEmbeddedIgnoredMigration0031SkipsASourceFailingMidway(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	beadsDir := filepath.Dir(dataDir)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	closed := false
	defer func() {
		if !closed {
			closeConn()
		}
	}()

	// First source: pre-3's row. Second (0062's own): pre-1's, two of pre-2's,
	// and pre-3's.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('pre-1', 'first', '', '', '', '', 'open', 2, 'task'),
  ('pre-2', 'second', '', '', '', '', 'open', 2, 'task'),
  ('pre-3', 'third', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-3', 'created', 'carol', '2026-07-03 09:00:00', 'legacy-3');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: first pre-flip source')")
	mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--allow-empty', '-m', ?)", eventsFlipDropMessage)
	handDrop := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-1', 'created', 'alice', '2026-07-01 10:00:00', 'legacy-1'),
  ('pre-2', 'created', 'bob', '2026-07-02 10:00:00', 'legacy-2a'),
  ('pre-2', 'updated', 'bob', '2026-07-02 11:00:00', 'legacy-2b');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: second pre-flip source')")
	adoptPreflipLineage(t, ctx, conn)
	var second string
	if err := conn.QueryRowContext(ctx, `
SELECT a.parent_hash FROM dolt_commit_ancestors a JOIN dolt_log l ON l.commit_hash = a.commit_hash
WHERE l.message = ? AND l.commit_hash <> ? AND a.parent_index = 0`,
		eventsFlipDropMessage, handDrop).Scan(&second); err != nil {
		t.Fatalf("find 0062's drop commit parent: %v", err)
	}

	// One row per page: pre-2's second page from the second source drifts.
	defer schema.SetPreflipEventsBatchSizesForTest(1, 1)()
	pages, reads := 0, 0
	defer schema.SetPreflipEventsSourceFaultHookForTest(func(ref, issue string) error {
		reads++
		if ref == second && issue == "pre-2" {
			if pages++; pages == 2 {
				return errors.New("panic: invalid hash length: 19")
			}
		}
		return nil
	})()
	var notice bytes.Buffer
	defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp with a source failing midway: %v", err)
	}
	if pages != 2 {
		t.Fatalf("the second source's pre-2 was read in %d pages, want the failing 2nd", pages)
	}
	got := liveEvents(t, ctx, conn)
	requireContent(t, got, map[string]int{
		preflipNullDigest("pre-1", "created", "alice", "2026-07-01 10:00:00"): 1,
		preflipNullDigest("pre-3", "created", "carol", "2026-07-03 09:00:00"): 1,
	})
	requireDerivedIDs(t, got)
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after skipping the failing source")
	}
	warning := notice.String()
	for _, part := range []string{"Warning", second, "incomplete", "Restored 2 pre-upgrade audit event(s)"} {
		if !strings.Contains(warning, part) {
			t.Fatalf("restore notice = %q, want it to carry %q", warning, part)
		}
	}
	if strings.Contains(warning, handDrop) {
		t.Fatalf("restore notice = %q names the readable source's drop commit", warning)
	}

	closeConn()
	closed = true
	notice.Reset()
	reads = 0
	store, err := embeddeddolt.Open(ctx, beadsDir, preflipDatabase, "main")
	if err != nil {
		t.Fatalf("writable open after the skip: %v", err)
	}
	closeEmbeddedDoltStore(t, store)
	ro, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, preflipDatabase, "main")
	if err != nil {
		t.Fatalf("read-only open after the skip: %v", err)
	}
	closeEmbeddedDoltStore(t, ro)
	if reads != 0 || notice.Len() != 0 {
		t.Fatalf("later opens read history %d time(s) and printed %q; 0031 is done", reads, notice.String())
	}
}

// TestEmbeddedIgnoredMigration0031OpensAShallowClone pins that a shallow
// clone whose depth ends at 0062's drop commit opens. Dolt refuses to name the
// missing first parent ("Commit not found. You are using a shallow clone"),
// which used to fail the ignored pass, and so every open of the clone, until
// it was re-cloned in full. There is nothing local to restore from, so the
// restore records 0031 silently; one commit deeper, it restores as usual.
func TestEmbeddedIgnoredMigration0031OpensAShallowClone(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	originData, remoteURL := publishPreflipLineage(t, ctx)
	migrateAndPublishOrigin(t, ctx, originData)
	depth := 0
	withRawConn(t, originData, preflipDatabase, func(conn *sql.Conn) {
		rows, err := conn.QueryContext(ctx, "SELECT message FROM dolt_log")
		if err != nil {
			t.Fatalf("read origin log: %v", err)
		}
		defer rows.Close()
		for i := 1; rows.Next(); i++ {
			var message string
			if err := rows.Scan(&message); err != nil {
				t.Fatalf("scan origin log: %v", err)
			}
			if message == eventsFlipDropMessage {
				depth = i
			}
		}
	})
	if depth == 0 {
		t.Fatal("origin history holds no drop commit")
	}

	for _, tc := range []struct {
		name  string
		depth int
		want  map[string]int
	}{
		{"depth ends at the drop commit", depth, map[string]int{}},
		{"depth reaches its parent", depth + 1, preflipSeededContent()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			dataDir := filepath.Join(beadsDir, "embeddeddolt")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatalf("create clone data dir: %v", err)
			}
			withRawConn(t, dataDir, "", func(conn *sql.Conn) {
				mustExecOn(t, ctx, conn, "CALL DOLT_CLONE('--depth', ?, ?, ?)", fmt.Sprint(tc.depth), remoteURL, preflipDatabase)
			})
			var notice bytes.Buffer
			defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
			store, err := embeddeddolt.Open(ctx, beadsDir, preflipDatabase, "main")
			if err != nil {
				t.Fatalf("open shallow clone: %v", err)
			}
			closeEmbeddedDoltStore(t, store)
			withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
				requireContent(t, liveEvents(t, ctx, conn), tc.want)
				if !ignoredCursorHas(t, ctx, conn, 31) {
					t.Fatal("ignored cursor does not record 0031")
				}
			})
			if len(tc.want) == 0 && notice.Len() != 0 {
				t.Fatalf("restore notice = %q; a shallow clone has nothing to restore and nothing to warn about", notice.String())
			}
		})
	}
}

// TestEmbeddedIgnoredMigration0031SkipsAPersistentHistoryFailure pins that a
// failure reading history the classifier has never seen (here, a real failing
// AS OF statement on the migration's own pinned session, standing in for a
// missing chunk) skips that source with a warning instead of failing every
// open: bd reads nothing else that old, so the restore is the only thing such
// damage would break. The other source is still restored, 0031 records, what
// the session wrote after the failed statement survives a full reopen, and
// that reopen neither fails nor reads history again.
func TestEmbeddedIgnoredMigration0031SkipsAPersistentHistoryFailure(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	beadsDir := filepath.Dir(dataDir)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	closed := false
	defer func() {
		if !closed {
			closeConn()
		}
	}()

	// First source: pre-1's row, under a hand-made drop commit. Second
	// (0062's own): pre-1's and pre-2's rows.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('pre-1', 'first', '', '', '', '', 'open', 2, 'task'),
  ('pre-2', 'second', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-1', 'created', 'alice', '2026-07-01 10:00:00', 'legacy-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: first pre-flip source')")
	mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('--allow-empty', '-m', ?)", eventsFlipDropMessage)
	handDrop := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id) VALUES
  ('pre-2', 'updated', 'carol', '2026-07-02 09:00:00', 'legacy-5');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: second pre-flip source')")
	adoptPreflipLineage(t, ctx, conn)
	var second string
	if err := conn.QueryRowContext(ctx, `
SELECT a.parent_hash FROM dolt_commit_ancestors a JOIN dolt_log l ON l.commit_hash = a.commit_hash
WHERE l.message = ? AND l.commit_hash <> ? AND a.parent_index = 0`,
		eventsFlipDropMessage, handDrop).Scan(&second); err != nil {
		t.Fatalf("find 0062's drop commit parent: %v", err)
	}
	pre1 := preflipNullDigest("pre-1", "created", "alice", "2026-07-01 10:00:00")

	failures := 0
	restoreHook := schema.SetPreflipEventsSourceFaultHookForTest(func(ref, _ string) error {
		if ref != second {
			return nil
		}
		failures++
		rows, err := conn.QueryContext(ctx, "SELECT COUNT(*) FROM events AS OF 'v0v0v0v0v0v0v0v0v0v0v0v0v0v0v0v0'")
		if err == nil {
			rows.Close()
			return errors.New("the fixture's AS OF read of a missing commit succeeded")
		}
		return err
	})
	var notice bytes.Buffer
	restoreNotice := schema.SetPreflipEventsNoticeOutForTest(&notice)
	_, err := schema.MigrateUp(ctx, conn)
	restoreHook()
	restoreNotice()
	if err != nil {
		t.Fatalf("MigrateUp with a persistently unreadable source: %v", err)
	}
	if failures == 0 {
		t.Fatal("the second source was never read; the fixture failed nothing")
	}
	if warning := notice.String(); !strings.Contains(warning, "Warning") || !strings.Contains(warning, second) {
		t.Fatalf("restore notice = %q, want a warning naming %s", warning, second)
	}
	requireContent(t, liveEvents(t, ctx, conn), map[string]int{pre1: 1})
	if !ignoredCursorHas(t, ctx, conn, 31) {
		t.Fatal("ignored cursor does not record 0031 after skipping the unreadable source")
	}

	closeConn()
	closed = true
	notice.Reset()
	reads := 0
	defer schema.SetPreflipEventsSourceFaultHookForTest(func(string, string) error { reads++; return nil })()
	defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
	store, err := embeddeddolt.Open(ctx, beadsDir, preflipDatabase, "main")
	if err != nil {
		t.Fatalf("reopen after the skip: %v", err)
	}
	closeEmbeddedDoltStore(t, store)
	if reads != 0 || notice.Len() != 0 {
		t.Fatalf("the reopen read history %d time(s) and printed %q; 0031 is done", reads, notice.String())
	}
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		got := liveEvents(t, ctx, conn)
		requireContent(t, got, map[string]int{pre1: 1})
		requireDerivedIDs(t, got)
		if !ignoredCursorHas(t, ctx, conn, 31) {
			t.Fatal("the reopened clone does not record 0031")
		}
	})
}

// preflipStatementCounter counts the statements the restore issues.
type preflipStatementCounter struct {
	schema.DBConn
	statements, inserts int
}

func (c *preflipStatementCounter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.statements++
	if strings.HasPrefix(query, "INSERT") {
		c.inserts++
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

func (c *preflipStatementCounter) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.statements++
	return c.DBConn.QueryContext(ctx, query, args...)
}

func (c *preflipStatementCounter) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.statements++
	return c.DBConn.QueryRowContext(ctx, query, args...)
}

// seedManyPreflipEvents commits, on a database at main 61, issues events per
// issue, each with its own content, under legacy ids.
func seedManyPreflipEvents(t *testing.T, ctx context.Context, conn *sql.Conn, issues, events int) {
	t.Helper()
	var issueRows, eventRows strings.Builder
	issueRows.WriteString("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ")
	eventRows.WriteString("INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES ")
	for i := range issues {
		if i > 0 {
			issueRows.WriteString(", ")
		}
		fmt.Fprintf(&issueRows, "('many-%03d', 'busy', '', '', '', '', 'open', 2, 'task')", i)
		for j := range events {
			if i > 0 || j > 0 {
				eventRows.WriteString(", ")
			}
			fmt.Fprintf(&eventRows, "('many-%03d', 'updated', 'tester', '{\"n\":%d}', NULL, NULL, '2026-07-30 00:00:%02d', 'legacy-%03d-%02d')", i, j, j, i, j)
		}
	}
	execFrozenGuard(t, ctx, conn, issueRows.String()+";\n"+eventRows.String()+";\n")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: many pre-flip events')")
}

// TestEmbeddedIgnoredMigration0031BatchesItsStatements pins the restore's
// cost: its statement count grows with the number of issues, never with the
// number of rows. It used to probe the live table once per missing row and
// write each issue separately, about 1 ms per event in embedded mode and more
// on a sql-server, where the restore holds the migration lock that every
// other writable open waits on for at most 30 s.
func TestEmbeddedIgnoredMigration0031BatchesItsStatements(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	const issues, events = 60, 12
	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedManyPreflipEvents(t, ctx, conn, issues, events)
	adoptPreflipLineage(t, ctx, conn)

	counter := &preflipStatementCounter{DBConn: conn}
	if err := schema.RestorePreflipEventsForTest(ctx, counter); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := liveEvents(t, ctx, conn)
	total := 0
	for _, ids := range got {
		total += len(ids)
	}
	if total != issues*events {
		t.Fatalf("restored %d events, want %d", total, issues*events)
	}
	requireDerivedIDs(t, got)
	// One read per issue, plus a handful per page and per batch (75 today;
	// probing and writing per row took 908).
	if limit := issues + 30; counter.statements > limit {
		t.Fatalf("restoring %d events of %d issues took %d statements, want at most %d", issues*events, issues, counter.statements, limit)
	}
	if counter.inserts != 1 {
		t.Fatalf("restore wrote %d INSERTs for %d rows, want 1", counter.inserts, issues*events)
	}
}

// TestEmbeddedIgnoredMigration0031SplitsInsertsByContentSize pins the byte
// budget on one restore INSERT: update events carry the whole issue as JSON,
// so a batch of them must not grow into one oversized statement. Under a
// budget smaller than any row, every row is its own INSERT, and the result is
// unchanged.
func TestEmbeddedIgnoredMigration0031SplitsInsertsByContentSize(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedPreflipEvents(t, ctx, conn)
	adoptPreflipLineage(t, ctx, conn)

	defer schema.SetPreflipEventsInsertBytesForTest(1)()
	inserts := 0
	defer schema.SetPreflipEventsInsertFaultHookForTest(func() error { inserts++; return nil })()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (restore): %v", err)
	}
	got := liveEvents(t, ctx, conn)
	requireContent(t, got, preflipSeededContent())
	requireDerivedIDs(t, got)
	if inserts != 5 {
		t.Fatalf("restore wrote %d INSERTs, want one per row (5) under a 1-byte budget", inserts)
	}
}
