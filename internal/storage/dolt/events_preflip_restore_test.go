package dolt

import (
	"bytes"
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestPreflipEventsRestoreOnSQLServer runs the pre-upgrade events restore
// (ignored migration 0031, gastownhall/beads#7039) through the server-mode
// migration path, MigrateUpWithLock on a pinned session of a real dolt
// sql-server, where the #7039 reporter's fleet runs: a database that ran main
// migration 0062 itself, then lost its live rows the way an adopting clone
// starts (events empty, 0031 pending). The restore must bring back every
// pre-upgrade content at its multiplicity under the content-derived ids, skip
// and count the events of an issue deleted since, make no commit, leave
// dolt_status as it was, and be visible to the pool's other sessions. A
// second open is the converged fast path and changes nothing.
func TestPreflipEventsRestoreOnSQLServer(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	dbName := uniqueTestDBName(t)
	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	// No DROP DATABASE on cleanup: rapid CREATE/DROP cycles crash the Dolt
	// server (see setupPreReshapeAncestor).
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	db, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName, Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %.60q: %v", query, err)
		}
	}
	scalar := func(query string, args ...any) string {
		t.Helper()
		var v string
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&v); err != nil {
			t.Fatalf("%.60q: %v", query, err)
		}
		return v
	}
	status := func() []string {
		t.Helper()
		rows, err := conn.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status ORDER BY table_name, staged")
		if err != nil {
			t.Fatalf("read dolt_status: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var table, state string
			var staged bool
			if err := rows.Scan(&table, &staged, &state); err != nil {
				t.Fatalf("scan dolt_status: %v", err)
			}
			if table == "events" {
				t.Fatalf("events shows in dolt_status as %q; the restore must leave it dolt-ignored", state)
			}
			out = append(out, table+" "+state)
		}
		return out
	}

	// A pre-flip lineage: events still versioned, three issues, an exact
	// duplicate pair, NULL next to the empty string, and an issue that is
	// deleted after the upgrade.
	if _, err := schema.MigrateUpTo(ctx, conn, 61); err != nil {
		t.Fatalf("MigrateUpTo(61): %v", err)
	}
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: schema at 61')")
	exec(`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('srv-1', 'first', '', '', '', '', 'open', 2, 'task'),
  ('srv-2', 'second', '', '', '', '', 'open', 2, 'task'),
  ('srv-gone', 'deleted after the upgrade', '', '', '', '', 'open', 2, 'task')`)
	exec(`INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES
  ('srv-1', 'created', 'alice', NULL, NULL, NULL, '2026-07-01 10:00:00', 'legacy-1'),
  ('srv-1', 'commented', 'bob', NULL, NULL, 'same words', '2026-07-01 11:00:00', 'legacy-2'),
  ('srv-1', 'commented', 'bob', NULL, NULL, 'same words', '2026-07-01 11:00:00', 'legacy-3'),
  ('srv-2', 'updated', 'carol', '', NULL, '', '2026-07-02 09:00:00', 'legacy-4'),
  ('srv-gone', 'created', 'dave', NULL, NULL, NULL, '2026-07-03 09:00:00', 'legacy-5'),
  ('srv-gone', 'closed', 'dave', NULL, NULL, NULL, '2026-07-03 10:00:00', 'legacy-6')`)
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: pre-flip events')")

	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	null := sql.NullString{}
	digest := func(f ...sql.NullString) string { return rowid.Digest(f) }
	want := map[string]int{
		digest(v("srv-1"), v("created"), v("alice"), null, null, null, v("2026-07-01 10:00:00")):            1,
		digest(v("srv-1"), v("commented"), v("bob"), null, null, v("same words"), v("2026-07-01 11:00:00")): 2,
		digest(v("srv-2"), v("updated"), v("carol"), v(""), null, v(""), v("2026-07-02 09:00:00")):          1,
	}

	// The migrator's own pass runs 0062 (and 0031, which finds nothing
	// missing). Then the issue is deleted, and the live rows go, which leaves
	// the adopting clone's state with the 0031 marker pending.
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("MigrateUpWithLock (migrator): %v", err)
	}
	exec("DELETE FROM issues WHERE id = 'srv-gone'")
	exec("CALL DOLT_COMMIT('-Am', 'test: delete srv-gone after the upgrade')")
	exec("DELETE FROM events")
	exec("DELETE FROM ignored_schema_migrations WHERE version = 31")

	head := scalar("SELECT HASHOF('HEAD')")
	statusBefore := status()
	var notice bytes.Buffer
	defer schema.SetPreflipEventsNoticeOutForTest(&notice)()
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("MigrateUpWithLock (restore): %v", err)
	}

	got := readServerEventContent(ctx, t, conn)
	if len(got) != len(want) {
		t.Fatalf("live events hold %d contents, want %d: %v", len(got), len(want), got)
	}
	for d, n := range want {
		ids := got[d]
		if len(ids) != n {
			t.Fatalf("content %s has %d live rows %v, want %d", d[:12], len(ids), ids, n)
		}
		derived := make([]string, n)
		for i := range derived {
			derived[i] = rowid.New("events", i, d)
		}
		slices.Sort(derived)
		if !slices.Equal(ids, derived) {
			t.Fatalf("content %s ids = %v, want the derived ids %v", d[:12], ids, derived)
		}
	}
	for _, line := range []string{
		"Restored 4 pre-upgrade audit event(s)",
		"Skipped 2 pre-upgrade audit event(s) whose issue id no longer exists",
	} {
		if !strings.Contains(notice.String(), line) {
			t.Fatalf("restore notice = %q, want it to report %q", notice.String(), line)
		}
	}
	if scalar("SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 31") != "1" {
		t.Fatal("ignored cursor does not record 0031 after the restore")
	}
	if got := scalar("SELECT HASHOF('HEAD')"); got != head {
		t.Fatalf("HEAD moved from %s to %s; the restore must not commit", head, got)
	}
	if after := status(); !slices.Equal(after, statusBefore) {
		t.Fatalf("dolt_status = %v after the restore, want it as it was: %v", after, statusBefore)
	}
	var other int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&other); err != nil {
		t.Fatalf("count events from another session: %v", err)
	}
	if other != 4 {
		t.Fatalf("another session sees %d events, want the 4 restored", other)
	}

	notice.Reset()
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("MigrateUpWithLock (converged): %v", err)
	}
	if notice.Len() != 0 {
		t.Fatalf("a converged open printed %q; 0031 is done", notice.String())
	}
	if n := len(readServerEventContent(ctx, t, conn)); n != len(want) {
		t.Fatalf("a converged open changed the live events: %d contents", n)
	}
}

// readServerEventContent reads the live events table as digest -> sorted ids.
func readServerEventContent(ctx context.Context, t *testing.T, conn *sql.Conn) map[string][]string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT id, issue_id, event_type, actor, old_value, new_value, comment, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s') FROM events")
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
		d := rowid.Digest(fields)
		out[d] = append(out[d], id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read live events: %v", err)
	}
	for _, ids := range out {
		slices.Sort(ids)
	}
	return out
}
