package dolt

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestDepExternalRetypeRepairOnSQLServer runs the 0032 re-type repair
// (gastownhall/beads#8299) through the server-mode migration path,
// MigrateUpWithLock on a pinned session of a real dolt sql-server, where a
// multi-rig fleet holds the legacy shape: a database fully migrated except
// the marker, holding an external row whose target lives in the same
// database. A fault injected after the repair's conversions but before its
// transaction commit must roll the whole repair back — row, blocked state,
// marker and history exactly as they were — and a plain retry must converge
// to the re-typed edge with exactly one new commit, the pass's staged one.
func TestDepExternalRetypeRepairOnSQLServer(t *testing.T) {
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
	scalarInt := func(query string, args ...any) int {
		t.Helper()
		var v int
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&v); err != nil {
			t.Fatalf("%.60q: %v", query, err)
		}
		return v
	}
	status := func() []string {
		t.Helper()
		rows, err := conn.QueryContext(ctx, "SELECT table_name, status FROM dolt_status ORDER BY table_name")
		if err != nil {
			t.Fatalf("read dolt_status: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var table, state string
			if err := rows.Scan(&table, &state); err != nil {
				t.Fatalf("scan dolt_status: %v", err)
			}
			out = append(out, table+" "+state)
		}
		return out
	}

	// The fleet's state: fully migrated, then the legacy row written by a
	// pre-repair binary (raw insert; AddDependency would classify it), and
	// committed so the dirty-table guard is silent.
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("MigrateUpWithLock (migrator): %v", err)
	}
	const source = "srv-src"
	const target = "ops-tgt"
	rowID := depid.New(source, target)
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 's', '', '', '', '', 'open', 2, 'task')", source)
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 't', '', '', '', '', 'open', 2, 'task')", target)
	exec(`INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_at, created_by, metadata)
VALUES (?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())`, rowID, source, target)
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: legacy external dep row')")
	exec("DELETE FROM ignored_schema_migrations WHERE version >= 32")

	logBefore := scalarInt("SELECT COUNT(*) FROM dolt_log")
	headBefore := scalar("SELECT HASHOF('HEAD')")
	statusBefore := status()

	// The fault: after the conversions, before the repair transaction's
	// commit — the seam every mid-repair failure funnels through on the
	// server path too.
	errInjected := errors.New("injected fault after the 0032 conversions")
	restoreHook := schema.SetDepRetypeFaultHookForTest(func(_ context.Context, _ schema.DBConn, stage string) error {
		if stage == "converted" {
			return errInjected
		}
		return nil
	})
	_, err = schema.MigrateUpWithLock(ctx, conn, dbName)
	restoreHook()
	if !errors.Is(err, errInjected) {
		t.Fatalf("MigrateUpWithLock (fault) error = %v, want the injected fault", err)
	}

	// No debris: row, blocked state, marker, history, working set.
	if got := scalar("SELECT COALESCE(depends_on_external, '') FROM dependencies WHERE id = ?", rowID); got != target {
		t.Fatalf("row %s external target after the fault = %q, want the seeded %q untouched", rowID, got, target)
	}
	if got := scalar("SELECT COALESCE(depends_on_issue_id, '') FROM dependencies WHERE id = ?", rowID); got != "" {
		t.Fatalf("row %s gained a typed target across the rolled-back repair: %q", rowID, got)
	}
	if got := scalar("SELECT is_blocked FROM issues WHERE id = ?", source); got != "0" {
		t.Fatalf("source %s is_blocked = %s after the fault, want 0", source, got)
	}
	if got := scalar("SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); got != "0" {
		t.Fatalf("marker 32 count after the fault = %s, want 0: the next open must retry the repair", got)
	}
	if got := scalarInt("SELECT COUNT(*) FROM dolt_log"); got != logBefore {
		t.Fatalf("dolt_log count = %d after the fault, want the pre-pass %d", got, logBefore)
	}
	if got := scalar("SELECT HASHOF('HEAD')"); got != headBefore {
		t.Fatalf("HEAD moved from %s to %s across the failed pass", headBefore, got)
	}
	if got := status(); !slices.Equal(got, statusBefore) {
		t.Fatalf("dolt_status after the fault = %v, want the pre-pass %v", got, statusBefore)
	}

	// The retry converges, with exactly the pass's one staged commit.
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("MigrateUpWithLock (retry): %v", err)
	}
	if got := scalar("SELECT COALESCE(depends_on_issue_id, '') FROM dependencies WHERE id = ?", rowID); got != target {
		t.Fatalf("row %s typed target after the retry = %q, want %q", rowID, got, target)
	}
	if got := scalar("SELECT COALESCE(depends_on_external, '') FROM dependencies WHERE id = ?", rowID); got != "" {
		t.Fatalf("row %s kept its external column after the retry: %q", rowID, got)
	}
	if got := scalar("SELECT is_blocked FROM issues WHERE id = ?", source); got != "1" {
		t.Fatalf("source %s is_blocked = %s after the retry, want 1", source, got)
	}
	if got := scalar("SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); got != "1" {
		t.Fatalf("marker 32 count after the retry = %s, want 1", got)
	}
	if got := scalarInt("SELECT COUNT(*) FROM dolt_log"); got != logBefore+1 {
		t.Fatalf("dolt_log count = %d after the retry, want exactly one new commit (%d+1)", got, logBefore)
	}
	if got := status(); !slices.Equal(got, statusBefore) {
		t.Fatalf("dolt_status after the retry = %v, want the pre-pass %v", got, statusBefore)
	}
}
