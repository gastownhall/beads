//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests cover gastownhall/beads#7073 against the real engine: a lineage
// whose HEAD carries a __temp__ table-rebuild intermediate that an older
// binary committed (a @@dolt_transaction_commit=1 auto-commit, or a blanket
// DOLT_ADD('-A') from before the __temp__% pattern shipped).
//
// The ignored-lane cursor is clone-local, so every fresh clone of such a
// lineage replays the ignored series from zero on its first open, and
// ignored/0002 renames the HEAD-tracked __temp__wisp_child_counters onto the
// ignored final name. Nothing can stage that rename, and the next pull dies
// with Dolt's "malformed database: chunk … required but not found". The
// fixtures and the red/green shape are adapted from the reporter's probe
// (@bee-ghosttrack, #7073).

const tempResidueTable = "__temp__wisp_child_counters"

// tempResidueLineage is a hub whose HEAD carries tempResidueTable (or, for
// the control, does not), pushed to a file remote a clone can be made from.
type tempResidueLineage struct {
	hub       *pristineEmbeddedDoltFixture
	remoteURL string
}

// newTempResidueLineage commits the intermediate the way a pre-#6993 binary
// did: with no __temp__% pattern in dolt_ignore, so a blanket add takes it.
func newTempResidueLineage(t *testing.T, database string, residue bool) *tempResidueLineage {
	t.Helper()
	ctx := t.Context()
	hub := newPristineEmbeddedDoltFixture(t, database)
	closeEmbeddedDoltStore(t, hub.store)

	remote := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remote, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remote
	withRawConn(t, hub.dataDir, hub.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "DELETE FROM dolt_ignore WHERE pattern='__temp__%'")
		if residue {
			mustExecOn(t, ctx, conn, "CREATE TABLE "+tempResidueTable+
				" (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
		}
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'auto-commit caught the rebuild intermediate')")
		if residue && !trackedAtHead(t, ctx, conn, tempResidueTable) {
			t.Fatalf("fixture did not commit %s into HEAD; the wedge cannot be reproduced", tempResidueTable)
		}
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	return &tempResidueLineage{hub: hub, remoteURL: remoteURL}
}

// clone makes a fresh clone of the lineage WITHOUT opening it through bd, so
// the first bd open is the one under test. It returns the clone's beads and
// data directories.
func (l *tempResidueLineage) clone(t *testing.T) (string, string) {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), "clone")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	withRawConn(t, dataDir, "", func(conn *sql.Conn) {
		mustExecOn(t, t.Context(), conn, "CALL DOLT_CLONE(?, ?)", l.remoteURL, l.hub.database)
	})
	return beadsDir, dataDir
}

// publish lands one more hub commit on the remote, carrying a marker row the
// clone must receive by pulling.
func (l *tempResidueLineage) publish(t *testing.T, key, value string) {
	t.Helper()
	ctx := t.Context()
	withRawConn(t, l.hub.dataDir, l.hub.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES (?, ?)", key, value)
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'hub: publish a marker')")
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
}

func openAndClose(t *testing.T, beadsDir, database string) {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open %s: %v", beadsDir, err)
	}
	closeEmbeddedDoltStore(t, store)
}

// pullOnce opens the store writable (which migrates, and so reconciles) and
// pulls, returning the pull's error.
func pullOnce(t *testing.T, beadsDir, database string) error {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open %s: %v", beadsDir, err)
	}
	pullErr := store.Pull(t.Context())
	closeEmbeddedDoltStore(t, store)
	return pullErr
}

func configValue(t *testing.T, ctx context.Context, conn *sql.Conn, key string) string {
	t.Helper()
	var value string
	if err := conn.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value); err != nil {
		t.Fatalf("reading config %q: %v", key, err)
	}
	return value
}

// residueStatus is every dolt_status row that mentions a __temp__ name: a
// delete, a rename half, or a modification of a tracked intermediate. Any of
// them is a non-add delta Dolt's merge refuses.
func residueStatus(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	var out []string
	for _, name := range dirtyTableNames(t, ctx, conn) {
		if strings.Contains(name, "__temp__") {
			out = append(out, name)
		}
	}
	return out
}

// TestFreshCloneOfTempResidueLineagePulls is the issue's red/green probe: a
// fresh clone of a lineage that tracks __temp__wisp_child_counters at HEAD
// opens writable, then pulls a new hub commit. Before the fix the open
// renamed the tracked intermediate onto the ignored final name and the pull
// failed with "malformed database"; the residue=false control proves the
// fixture itself pulls.
func TestFreshCloneOfTempResidueLineagePulls(t *testing.T) {
	for _, residue := range []bool{false, true} {
		t.Run(fmt.Sprintf("residue=%v", residue), func(t *testing.T) {
			ctx := t.Context()
			lineage := newTempResidueLineage(t, "residueclone", residue)
			cloneBeads, cloneData := lineage.clone(t)
			database := lineage.hub.database

			openAndClose(t, cloneBeads, database)
			withRawConn(t, cloneData, database, func(conn *sql.Conn) {
				if trackedAtHead(t, ctx, conn, tempResidueTable) {
					t.Errorf("%s is still tracked at HEAD after the clone's first writable open", tempResidueTable)
				}
				if residue := residueStatus(t, ctx, conn); len(residue) > 0 {
					t.Errorf("dolt_status after first open carries __temp__ residue %v; pull would refuse it", residue)
				}
			})

			lineage.publish(t, "hub.marker", "from the hub")
			if err := pullOnce(t, cloneBeads, database); err != nil {
				t.Fatalf("fresh clone of a lineage (residue=%v) cannot pull: %v", residue, err)
			}
			withRawConn(t, cloneData, database, func(conn *sql.Conn) {
				if got := configValue(t, ctx, conn, "hub.marker"); got != "from the hub" {
					t.Errorf("pulled hub marker = %q, want %q", got, "from the hub")
				}
				if trackedAtHead(t, ctx, conn, tempResidueTable) {
					t.Errorf("%s is tracked at HEAD again after the pull", tempResidueTable)
				}
				if residue := residueStatus(t, ctx, conn); len(residue) > 0 {
					t.Errorf("dolt_status after pull carries __temp__ residue %v", residue)
				}
			})
		})
	}
}

// statusOf returns the dolt_status rows for names containing needle, as
// "table_name staged=<bool> status", for preconditions and diagnostics.
func statusOf(t *testing.T, ctx context.Context, conn *sql.Conn, needle string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status")
	if err != nil {
		t.Fatalf("reading dolt_status: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name, status string
		var staged bool
		if err := rows.Scan(&name, &staged, &status); err != nil {
			t.Fatalf("reading dolt_status: %v", err)
		}
		if strings.Contains(name, needle) {
			out = append(out, fmt.Sprintf("%s staged=%v %s", name, staged, status))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading dolt_status: %v", err)
	}
	return out
}

func childCounterRows(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT parent_id, last_child FROM wisp_child_counters ORDER BY parent_id")
	if err != nil {
		t.Fatalf("reading wisp_child_counters: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var parent string
		var last int
		if err := rows.Scan(&parent, &last); err != nil {
			t.Fatalf("reading wisp_child_counters: %v", err)
		}
		out = append(out, fmt.Sprintf("%s=%d", parent, last))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading wisp_child_counters: %v", err)
	}
	return out
}

// TestTempResidueRenamedStoreConvergesOnOpen covers a store that already
// reached the wedge before upgrading: the HEAD-tracked intermediate has been
// renamed onto wisp_child_counters, which holds live rows. One writable open
// must untrack the old name without touching the final table, after which
// the pull goes through.
func TestTempResidueRenamedStoreConvergesOnOpen(t *testing.T) {
	ctx := t.Context()
	lineage := newTempResidueLineage(t, "residuerenamed", true)
	hub := lineage.hub

	// A peer that never ran bd publishes a commit for the wedged store to pull.
	_, writerData := lineage.clone(t)
	withRawConn(t, writerData, hub.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES ('writer.marker', 'from the writer')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'writer: publish a marker')")
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	// Drive the store into the field's renamed state: what the pre-fix ignored
	// series did on a fresh clone, then rows written through the final name.
	want := []string{"wedge-a=3", "wedge-b=9"}
	withRawConn(t, hub.dataDir, hub.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "INSERT INTO dolt_ignore (pattern, ignored) VALUES ('__temp__%', 1)")
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('dolt_ignore')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'reseed __temp__%')")
		mustExecOn(t, ctx, conn, "DROP TABLE wisp_child_counters")
		mustExecOn(t, ctx, conn, "RENAME TABLE "+tempResidueTable+" TO wisp_child_counters")
		mustExecOn(t, ctx, conn, "INSERT INTO wisp_child_counters (parent_id, last_child) VALUES ('wedge-a', 3), ('wedge-b', 9)")
		got := statusOf(t, ctx, conn, tempResidueTable)
		if len(got) != 1 || !strings.Contains(got[0], "renamed") {
			t.Fatalf("dolt_status for %s = %v, want the unstageable rename the field reports", tempResidueTable, got)
		}
		if !trackedAtHead(t, ctx, conn, tempResidueTable) {
			t.Fatalf("%s is not tracked at HEAD; the wedge is not reproduced", tempResidueTable)
		}
	})

	openAndClose(t, hub.beadsDir, hub.database)
	withRawConn(t, hub.dataDir, hub.database, func(conn *sql.Conn) {
		if trackedAtHead(t, ctx, conn, tempResidueTable) {
			t.Errorf("%s is still tracked at HEAD after a writable open", tempResidueTable)
		}
		if got := statusOf(t, ctx, conn, tempResidueTable); len(got) > 0 {
			t.Errorf("dolt_status still carries %v after a writable open", got)
		}
		if got := childCounterRows(t, ctx, conn); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("wisp_child_counters rows after the repair = %v, want %v", got, want)
		}
	})

	if err := pullOnce(t, hub.beadsDir, hub.database); err != nil {
		t.Fatalf("pull after the repair failed: %v", err)
	}
	withRawConn(t, hub.dataDir, hub.database, func(conn *sql.Conn) {
		if got := configValue(t, ctx, conn, "writer.marker"); got != "from the writer" {
			t.Errorf("pulled writer marker = %q, want %q", got, "from the writer")
		}
		if got := childCounterRows(t, ctx, conn); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("wisp_child_counters rows after the pull = %v, want %v", got, want)
		}
		if trackedAtHead(t, ctx, conn, tempResidueTable) {
			t.Errorf("%s is tracked at HEAD again after the pull", tempResidueTable)
		}
	})
}

// TestTempResidueUntrackKeepsRowsForTheirOwner covers an intermediate whose
// working rows are still owed to the operation that made it: an older
// binary's 0062 committed __temp__events_flip at HEAD after Phase 1 and died,
// so the copy holds an audit row the events table no longer has. Untracking
// must leave that copy for 0062's replay to restore, not drop it, and the
// pass must end with the intermediate gone from HEAD and the working set.
func TestTempResidueUntrackKeepsRowsForTheirOwner(t *testing.T) {
	ctx := t.Context()
	const flip = "__temp__events_flip"

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	mustExecOn(t, ctx, conn, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('flip-1', 'flip seed', '', '', '', '', 'open', 2, 'task')`)
	mustExecOn(t, ctx, conn, `INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('flip-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-live')`)
	mustExecOn(t, ctx, conn, `CREATE TABLE `+flip+` (
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value LONGTEXT,
    new_value LONGTEXT,
    comment TEXT,
    created_at DATETIME NOT NULL,
    id CHAR(36) NOT NULL,
    PRIMARY KEY (id)
)`)
	mustExecOn(t, ctx, conn, `INSERT INTO `+flip+` (issue_id, event_type, actor, created_at, id)
VALUES ('flip-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-live'),
       ('flip-1', 'updated', 'tester', '2026-07-30 00:00:01', 'evt-only-in-flip')`)
	mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-f', '-A')")
	mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'older binary: auto-commit mid 0062')")
	if !trackedAtHead(t, ctx, conn, flip) {
		t.Fatalf("%s is not tracked at HEAD; the residue is not reproduced", flip)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	if trackedAtHead(t, ctx, conn, flip) {
		t.Errorf("%s is still tracked at HEAD after MigrateUp", flip)
	}
	if tablePresent(t, ctx, conn, flip) {
		t.Errorf("%s is still in the working set; 0062 owns its cleanup", flip)
	}
	if got := statusOf(t, ctx, conn, "__temp__"); len(got) > 0 {
		t.Errorf("dolt_status still carries %v after MigrateUp", got)
	}
	var ids []string
	rows, err := conn.QueryContext(ctx, "SELECT id FROM events ORDER BY id")
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("reading events: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading events: %v", err)
	}
	_ = rows.Close()
	if got, want := strings.Join(ids, ","), "evt-live,evt-only-in-flip"; got != want {
		t.Errorf("events after MigrateUp = %s, want %s (the copy's rows restored by 0062)", got, want)
	}
}
