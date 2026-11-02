package dolt

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestServerMigrate0046And0047KeepIssueUpdatedAt is the server-mode half of
// gastownhall/beads#5981's AC-1/AC-2 (the embedded half is in
// internal/storage/embeddeddolt/migrate_updated_at_preservation_test.go).
// The updated_at guard reads timestamps and writes them back through the
// same driver connection, and a sql-server connection is a different driver
// path from the embedded one: go-sql-driver/mysql parses DATETIME text into
// time.Time (ParseTime) and renders the restore's bound timestamps into the
// SQL text client-side (InterpolateParams), so the restore and its
// compare-and-swap reach the server as string literals rather than typed
// values. A v45 store migrated to the latest version through that path must
// still keep every issue's historical updated_at, in the working set and in
// the committed history, while is_blocked follows the dependency graph.
func TestServerMigrate0046And0047KeepIssueUpdatedAt(t *testing.T) {
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
	// No DROP DATABASE on cleanup, as in cross_upgrade_merge_test.go: rapid
	// CREATE/DROP cycles crash the shared Dolt server, and orphans die with it.
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
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	// v45: the 1.0.x shape, before 0046 adds is_blocked.
	if _, err := schema.MigrateUpTo(ctx, conn, 45); err != nil {
		t.Fatalf("migrate to 0045: %v", err)
	}
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: v45 baseline schema')")

	// A distinct, second-precision, months-old updated_at per issue:
	//
	//	srv-blocked    -blocks-> srv-blocker-open    (blocked)
	//	srv-unblocked  -blocks-> srv-blocker-closed  (not blocked)
	//	srv-child      -parent-child-> srv-blocked   (blocked through its parent)
	//	srv-free                                     (no edges)
	type fixture struct {
		id, status  string
		stamp       time.Time
		wantBlocked bool
	}
	stamp := func(n int) time.Time { return time.Date(2025, time.March, 1+n, 9, 30, n, 0, time.UTC) }
	issues := []fixture{
		{id: "srv-blocker-open", status: "open", stamp: stamp(1)},
		{id: "srv-blocker-closed", status: "closed", stamp: stamp(2)},
		{id: "srv-blocked", status: "open", stamp: stamp(3), wantBlocked: true},
		{id: "srv-unblocked", status: "open", stamp: stamp(4)},
		{id: "srv-child", status: "open", stamp: stamp(5), wantBlocked: true},
		{id: "srv-free", status: "open", stamp: stamp(6)},
	}
	for _, r := range issues {
		var closedAt any
		if r.status == "closed" {
			closedAt = r.stamp
		}
		exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at) "+
			"VALUES (?, ?, '', '', '', '', ?, 2, 'task', ?, ?, ?)",
			r.id, "fixture "+r.id, r.status, r.stamp, r.stamp, closedAt)
	}
	for _, e := range [][3]string{
		{"srv-blocked", "srv-blocker-open", "blocks"},
		{"srv-unblocked", "srv-blocker-closed", "blocks"},
		{"srv-child", "srv-blocked", "parent-child"},
	} {
		exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (UUID(), ?, ?, ?, 'fixture')",
			e[0], e[1], e[2])
	}
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: v45 issues with historical updated_at')")

	// The production upgrade, across 0046 and 0047 to the latest version.
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	for _, table := range []string{"issues", "issues AS OF 'HEAD'"} {
		for _, r := range issues {
			var got time.Time
			var blocked bool
			if err := conn.QueryRowContext(ctx, "SELECT updated_at, is_blocked FROM "+table+" WHERE id = ?", r.id).Scan(&got, &blocked); err != nil {
				t.Fatalf("read %s %s: %v", table, r.id, err)
			}
			if !got.Equal(r.stamp) {
				t.Errorf("%s %s: updated_at = %s, want its historical %s (the migration overwrote it)",
					table, r.id, got.UTC().Format(time.RFC3339), r.stamp.Format(time.RFC3339))
			}
			if blocked != r.wantBlocked {
				t.Errorf("%s %s: is_blocked = %v, want %v", table, r.id, blocked, r.wantBlocked)
			}
		}
	}
}
