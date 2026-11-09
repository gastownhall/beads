//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin ignored migration 0032's upgrade repair (GH#8299): before
// it, a dependency whose target prefix differed from its source's was filed
// in depends_on_external by prefix alone, even when the target was an issue
// or wisp held in the same database, so the edge never blocked. The repair
// types every such row by where its target lives, in both dependency tables.
// Like the other migration tests they need BEADS_TEST_EMBEDDED_DOLT=1.

// legacyExternalDepsLegacyStamp is the updated_at every seeded row carries;
// the repair must leave it alone.
const legacyExternalDepsLegacyStamp = "2026-01-02 03:04:05"

// legacyExternalDep is one seeded dependency row.
type legacyExternalDep struct {
	table, source, target, depType string
}

// legacyExternalDepsDB creates a fully migrated database, then seeds and
// commits the rows a pre-fix binary wrote: cross-prefix edges onto local
// targets stored in depends_on_external. It removes the 0032 marker (if this
// binary has one) so the next MigrateUp runs the repair, and returns a pinned
// connection with its close function.
func legacyExternalDepsDB(t *testing.T, ctx context.Context, deps []legacyExternalDep, extraSQL string) (*sql.Conn, func()) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	withRawConn(t, dataDir, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CREATE DATABASE IF NOT EXISTS testdb")
	})
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("MigrateUp (fresh): %v", err)
	}
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, updated_at) VALUES
  ('xpa-1', 'issue source', '', '', '', '', 'open', 2, 'epic', '`+legacyExternalDepsLegacyStamp+`'),
  ('xpa-1.1', 'child of the issue source', '', '', '', '', 'open', 2, 'task', '`+legacyExternalDepsLegacyStamp+`'),
  ('xpa-2', 'source onto a wisp', '', '', '', '', 'open', 2, 'task', '`+legacyExternalDepsLegacyStamp+`'),
  ('xpa-3', 'source onto an absent target', '', '', '', '', 'open', 2, 'task', '`+legacyExternalDepsLegacyStamp+`'),
  ('xpa-4', 'source onto an external ref', '', '', '', '', 'open', 2, 'task', '`+legacyExternalDepsLegacyStamp+`'),
  ('xpb-1', 'open blocker in another prefix', '', '', '', '', 'open', 2, 'task', '`+legacyExternalDepsLegacyStamp+`');
INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, updated_at) VALUES
  ('xpb-w1', 'open wisp blocker in another prefix', '', '', '', '', 'open', 2, 'task', 1, '`+legacyExternalDepsLegacyStamp+`'),
  ('xpa-w2', 'wisp source', '', '', '', '', 'open', 2, 'task', 1, '`+legacyExternalDepsLegacyStamp+`');
`+extraSQL)
	mustExecOn(t, ctx, conn,
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (?, 'xpa-1.1', 'xpa-1', 'parent-child', ?, 'legacy-writer', '{}')",
		depid.New("xpa-1.1", "xpa-1"), legacyExternalDepsLegacyStamp)
	for _, d := range deps {
		mustExecOn(t, ctx, conn,
			"INSERT INTO "+d.table+" (id, issue_id, depends_on_external, type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, ?, 'legacy-writer', '{}')",
			depid.New(d.source, d.target), d.source, d.target, d.depType, legacyExternalDepsLegacyStamp)
	}
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: seed legacy external dependency rows')")
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version >= 32")
	return conn, closeConn
}

// legacyDepRow is a dependency row as the repair must leave it.
type legacyDepRow struct {
	ID, Issue, Wisp, External, CreatedAt, CreatedBy string
}

func readDepRow(t *testing.T, ctx context.Context, conn *sql.Conn, table, source, target string) legacyDepRow {
	t.Helper()
	var r legacyDepRow
	var issue, wisp, external sql.NullString
	err := conn.QueryRowContext(ctx,
		"SELECT id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s'), created_by FROM "+table+
			" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		source, target).Scan(&r.ID, &issue, &wisp, &external, &r.CreatedAt, &r.CreatedBy)
	if err != nil {
		t.Fatalf("read %s row %s -> %s: %v", table, source, target, err)
	}
	r.Issue, r.Wisp, r.External = issue.String, wisp.String, external.String
	return r
}

// requireDepTyped fails unless the row source -> target holds target in
// exactly column, under its unchanged id and provenance.
func requireDepTyped(t *testing.T, ctx context.Context, conn *sql.Conn, table, source, target, column string) {
	t.Helper()
	got := readDepRow(t, ctx, conn, table, source, target)
	want := legacyDepRow{ID: depid.New(source, target), CreatedAt: legacyExternalDepsLegacyStamp, CreatedBy: "legacy-writer"}
	switch column {
	case "depends_on_issue_id":
		want.Issue = target
	case "depends_on_wisp_id":
		want.Wisp = target
	default:
		want.External = target
	}
	if got != want {
		t.Fatalf("%s row %s -> %s = %+v, want %+v", table, source, target, got, want)
	}
}

func isBlockedRow(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) bool {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT is_blocked FROM "+table+" WHERE id = ?", id) == 1
}

// legacyRowsSnapshot is every issue, wisp and dependency row's full content,
// for the "a second open changes nothing" comparison.
func legacyRowsSnapshot(t *testing.T, ctx context.Context, conn *sql.Conn) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for table, query := range map[string]string{
		"issues":            "SELECT CONCAT_WS('|', id, is_blocked, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s')) FROM issues ORDER BY id",
		"wisps":             "SELECT CONCAT_WS('|', id, is_blocked, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s')) FROM wisps ORDER BY id",
		"dependencies":      "SELECT CONCAT_WS('|', id, issue_id, COALESCE(depends_on_issue_id, '-'), COALESCE(depends_on_wisp_id, '-'), COALESCE(depends_on_external, '-'), type) FROM dependencies ORDER BY id",
		"wisp_dependencies": "SELECT CONCAT_WS('|', id, issue_id, COALESCE(depends_on_issue_id, '-'), COALESCE(depends_on_wisp_id, '-'), COALESCE(depends_on_external, '-'), type) FROM wisp_dependencies ORDER BY id",
	} {
		rows, err := conn.QueryContext(ctx, query)
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				_ = rows.Close()
				t.Fatalf("snapshot %s: %v", table, err)
			}
			out[table] = append(out[table], s)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
	}
	return out
}

func doltCommitCount(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
}

func TestEmbeddedMigrateTypesLegacyLocalExternalDependencies(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, closeConn := legacyExternalDepsDB(t, ctx, []legacyExternalDep{
		{"dependencies", "xpa-1", "xpb-1", "blocks"},
		{"dependencies", "xpa-2", "xpb-w1", "blocks"},
		{"dependencies", "xpa-3", "zz-404", "blocks"},
		{"dependencies", "xpa-4", "external:other:capability", "blocks"},
		{"wisp_dependencies", "xpa-w2", "xpb-1", "blocks"},
	}, "")
	defer closeConn()

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (upgrade): %v", err)
	}

	requireDepTyped(t, ctx, conn, "dependencies", "xpa-1", "xpb-1", "depends_on_issue_id")
	requireDepTyped(t, ctx, conn, "dependencies", "xpa-2", "xpb-w1", "depends_on_wisp_id")
	requireDepTyped(t, ctx, conn, "wisp_dependencies", "xpa-w2", "xpb-1", "depends_on_issue_id")
	requireDepTyped(t, ctx, conn, "dependencies", "xpa-3", "zz-404", "depends_on_external")
	requireDepTyped(t, ctx, conn, "dependencies", "xpa-4", "external:other:capability", "depends_on_external")

	for _, c := range []struct {
		table, id string
		want      bool
	}{
		{"issues", "xpa-1", true},
		{"issues", "xpa-1.1", true}, // descendant of a newly blocked source
		{"issues", "xpa-2", true},
		{"wisps", "xpa-w2", true},
		{"issues", "xpb-1", false},
	} {
		if got := isBlockedRow(t, ctx, conn, c.table, c.id); got != c.want {
			t.Errorf("%s %s is_blocked = %v after the upgrade, want %v", c.table, c.id, got, c.want)
		}
	}
	for _, table := range []string{"issues", "wisps"} {
		if n := scalarInt(t, ctx, conn,
			"SELECT COUNT(*) FROM "+table+" WHERE DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s') <> ?", legacyExternalDepsLegacyStamp); n != 0 {
			t.Errorf("the repair restamped updated_at on %d %s rows", n, table)
		}
	}
	if !ignoredCursorHas(t, ctx, conn, 32) {
		t.Fatal("ignored marker 0032 was not recorded after a successful repair")
	}
	if n := scalarInt(t, ctx, conn,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name IN ('issues', 'dependencies')"); n != 0 {
		t.Fatalf("the repair left %d versioned tables uncommitted", n)
	}

	before := legacyRowsSnapshot(t, ctx, conn)
	commits := doltCommitCount(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp (second open): %v", err)
	}
	if after := legacyRowsSnapshot(t, ctx, conn); !reflect.DeepEqual(after, before) {
		t.Fatalf("a second open changed rows:\n before %v\n after  %v", before, after)
	}
	if got := doltCommitCount(t, ctx, conn); got != commits {
		t.Fatalf("a second open made %d Dolt commits", got-commits)
	}
}
