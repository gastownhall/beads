package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestTempResidueUntrackOnSQLServer runs the committed __temp__ intermediate
// repair (gastownhall/beads#7073) through the server-mode migration path:
// MigrateUpWithLock on a pinned session of a real dolt sql-server, against an
// at-latest database whose HEAD tracks a table-rebuild intermediate. The
// lock-free fast path must decline, the first call must untrack the
// intermediate (keeping its working rows) and count it, and the second call
// must find nothing to do and leave HEAD where it was. Both settings of
// @@dolt_transaction_commit run, since under 1 every SQL transaction commit is
// already a Dolt commit. Under 0 an unrelated staged edit must also stay out
// of the repair's commit and remain in the working set.
func TestTempResidueUntrackOnSQLServer(t *testing.T) {
	skipIfNoDolt(t)
	for _, txCommit := range []int{0, 1} {
		t.Run(fmt.Sprintf("dolt_transaction_commit=%d", txCommit), func(t *testing.T) {
			acquireTestSlot()
			t.Cleanup(releaseTestSlot)
			runTempResidueUntrackOnSQLServer(t, txCommit)
		})
	}
}

// tempResidueServerDB is one fresh database on the test sql-server: a pool for
// it and one pinned session, both closed on cleanup.
type tempResidueServerDB struct {
	t    *testing.T
	ctx  context.Context
	name string
	db   *sql.DB
	conn *sql.Conn
}

func newTempResidueServerDB(t *testing.T, ctx context.Context) *tempResidueServerDB {
	t.Helper()
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
	return &tempResidueServerDB{t: t, ctx: ctx, name: dbName, db: db, conn: conn}
}

func (s *tempResidueServerDB) exec(query string, args ...any) {
	s.t.Helper()
	if _, err := s.conn.ExecContext(s.ctx, query, args...); err != nil {
		s.t.Fatalf("exec %.60q: %v", query, err)
	}
}

func (s *tempResidueServerDB) scalar(q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string) string {
	s.t.Helper()
	var v string
	if err := q.QueryRowContext(s.ctx, query).Scan(&v); err != nil {
		s.t.Fatalf("%.60q: %v", query, err)
	}
	return v
}

func runTempResidueUntrackOnSQLServer(t *testing.T, txCommit int) {
	const residue = "__temp__wisp_child_counters"
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	s := newTempResidueServerDB(t, ctx)
	dbName, db, conn, exec, scalar := s.name, s.db, s.conn, s.exec, s.scalar

	// An at-latest database, then the older binary's residue: committed with
	// the pattern absent, and the pattern restored afterwards the way an
	// upgraded binary's seeding leaves it.
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("MigrateUpWithLock (bootstrap): %v", err)
	}
	exec("DELETE FROM dolt_ignore WHERE pattern = '__temp__%'")
	exec("CREATE TABLE " + residue + " (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
	exec("INSERT INTO " + residue + " VALUES ('srv-parent', 4)")
	exec("CALL DOLT_COMMIT('-Am', 'test: auto-commit caught the rebuild intermediate')")
	exec("INSERT INTO dolt_ignore (pattern, ignored) VALUES ('__temp__%', 1)")
	exec("CALL DOLT_ADD('dolt_ignore')")
	exec("CALL DOLT_COMMIT('-m', 'test: reseed __temp__%')")
	// An unrelated STAGED edit, the scoping hazard: the repair must clear it
	// from the staging area without discarding it, and its commit must not
	// pick it up. Only assertable with @@dolt_transaction_commit=0: under 1
	// the mode itself publishes every non-ignored working delta at every
	// statement boundary, with or without this repair.
	if txCommit == 0 {
		exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('unrelated-staged', 'staged edit', '', '', '', '', 'open', 2, 'task')")
		exec("CALL DOLT_ADD('issues')")
	}
	exec(fmt.Sprintf("SET @@dolt_transaction_commit = %d", txCommit))

	// headTemps counts the __temp__ tables at HEAD as q's session sees them.
	headTemps := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) int {
		t.Helper()
		rows, err := q.QueryContext(ctx, "SHOW TABLES AS OF 'HEAD' LIKE '__temp__%'")
		if err != nil {
			t.Fatalf("list __temp__ tables at HEAD: %v", err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("list __temp__ tables at HEAD: %v", err)
		}
		return n
	}
	if headTemps(conn) != 1 {
		t.Fatalf("%s is not tracked at HEAD; the residue is not reproduced", residue)
	}

	first, err := schema.MigrateUpWithLock(ctx, conn, dbName)
	if err != nil {
		t.Fatalf("MigrateUpWithLock (repair): %v", err)
	}
	if first == 0 {
		t.Error("the repairing call returned 0; a call that moved HEAD must count it")
	}
	atHead := headTemps(db)
	if atHead != 0 {
		t.Errorf("another session still sees %d __temp__ table(s) at HEAD after the repair", atHead)
	}
	if got := scalar(conn, "SELECT COUNT(*) FROM dolt_status WHERE LOCATE('__temp__', table_name) > 0"); got != "0" {
		t.Errorf("dolt_status carries %s __temp__ row(s) after the repair", got)
	}
	if got := scalar(conn, "SELECT last_child FROM "+residue+" WHERE parent_id = 'srv-parent'"); got != "4" {
		t.Errorf("%s working row = %s, want 4: the repair must keep the rows", residue, got)
	}
	if txCommit == 0 {
		if got := scalar(conn, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = 'unrelated-staged'"); got != "0" {
			t.Errorf("unrelated row committed at HEAD (%s), want 0: the repair's commit is scoped to the residue", got)
		}
		if got := scalar(conn, "SELECT COUNT(*) FROM issues WHERE id = 'unrelated-staged'"); got != "1" {
			t.Errorf("unrelated working rows after the repair = %s, want 1: the unstage must not discard them", got)
		}
		const want = "schema: untrack committed __temp__ table-rebuild intermediates so dolt_ignore can apply (gastownhall/beads#7073)"
		if got := scalar(conn, "SELECT message FROM dolt_log LIMIT 1"); got != want {
			t.Errorf("latest commit message = %q, want the scoped untrack %q", got, want)
		}
	}

	head := scalar(conn, "SELECT HASHOF('HEAD')")
	second, err := schema.MigrateUpWithLock(ctx, conn, dbName)
	if err != nil {
		t.Fatalf("MigrateUpWithLock (converged): %v", err)
	}
	if second != 0 {
		t.Errorf("the second call returned %d, want 0 on a repaired database", second)
	}
	if got := scalar(conn, "SELECT HASHOF('HEAD')"); got != head {
		t.Errorf("the second call moved HEAD from %s to %s", head, got)
	}
}

// TestTempResidueGlobVetoStaysVersionedOnSQLServer pins the wildcard veto
// through both gate callers on a real sql-server. Beside the seeded
// ('__temp__%', 1), an operator's ('__temp__keep*', 0) makes Dolt's own
// staging TRACK __temp__keep_rows: Dolt reads the pattern as a glob, which SQL
// LIKE cannot, and keeps the more specific of the two. The lock-free fast
// path must stay armed (the migration gate runs only on the locked path, so it
// is never consulted), a direct MigrateUp must find nothing to repair, and
// HEAD must keep the table throughout.
func TestTempResidueGlobVetoStaysVersionedOnSQLServer(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	const kept = "__temp__keep_rows"
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	s := newTempResidueServerDB(t, ctx)

	if _, err := schema.MigrateUpWithLock(ctx, s.conn, s.name); err != nil {
		t.Fatalf("MigrateUpWithLock (bootstrap): %v", err)
	}
	s.exec("INSERT INTO dolt_ignore (pattern, ignored) VALUES ('__temp__keep*', 0)")
	s.exec("CREATE TABLE " + kept + " (id INT NOT NULL PRIMARY KEY, note VARCHAR(64) NOT NULL)")
	s.exec("INSERT INTO " + kept + " VALUES (1, 'versioned on purpose')")
	// A plain -A, not a forced add: Dolt's own resolution decides what it takes.
	s.exec("CALL DOLT_COMMIT('-Am', 'test: version an intermediate on purpose')")

	// trackedAtHead reports whether q's session lists kept at HEAD.
	trackedAtHead := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) bool {
		t.Helper()
		rows, err := q.QueryContext(ctx, "SHOW TABLES AS OF 'HEAD' LIKE '__temp__%'")
		if err != nil {
			t.Fatalf("list __temp__ tables at HEAD: %v", err)
		}
		defer rows.Close()
		found := false
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("list __temp__ tables at HEAD: %v", err)
			}
			found = found || name == kept
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("list __temp__ tables at HEAD: %v", err)
		}
		return found
	}
	if !trackedAtHead(s.conn) {
		t.Fatalf("Dolt did not track %s under ('__temp__keep*', 0); the veto is not reproduced", kept)
	}
	head := s.scalar(s.conn, "SELECT HASHOF('HEAD')")

	lockedPath := 0
	applied, err := schema.MigrateUpWithLock(ctx, s.conn, s.name, schema.WithMigrationGate(
		func(context.Context, *sql.Conn) error {
			lockedPath++
			return nil
		}))
	if err != nil {
		t.Fatalf("MigrateUpWithLock: %v", err)
	}
	if lockedPath != 0 {
		t.Errorf("MigrateUpWithLock took the locked path: the fast path declined a deliberately versioned intermediate")
	}
	if applied != 0 {
		t.Errorf("MigrateUpWithLock returned %d, want 0: the veto leaves nothing to repair", applied)
	}

	applied, err = schema.MigrateUp(ctx, s.conn)
	if err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	if applied != 0 {
		t.Errorf("MigrateUp returned %d, want 0: the veto leaves nothing to repair", applied)
	}

	if got := s.scalar(s.conn, "SELECT HASHOF('HEAD')"); got != head {
		t.Errorf("HEAD moved from %s to %s under the operator's veto", head, got)
	}
	if !trackedAtHead(s.db) {
		t.Errorf("another session no longer sees %s at HEAD: the reconcile committed away a table the veto keeps versioned", kept)
	}
	if got := s.scalar(s.conn, "SELECT note FROM "+kept+" WHERE id = 1"); got != "versioned on purpose" {
		t.Errorf("%s working row = %q, want it untouched", kept, got)
	}
}
