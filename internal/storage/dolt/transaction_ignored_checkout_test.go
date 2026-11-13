package dolt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestBeginTxOnConnSkipsCheckoutWhenAlreadyOnBranch pins that the fresh-dial
// fallback does not issue DOLT_CHECKOUT when the session already sits on the
// requested branch. A fresh session lands on the database's default branch, so
// this is the normal case — and a capped operator user that may EXECUTE only
// dolt_add/dolt_commit is denied DOLT_CHECKOUT outright.
func TestBeginTxOnConnSkipsCheckoutWhenAlreadyOnBranch(t *testing.T) {
	ctx := context.Background()
	db, drv := openMockDB(t)
	drv.failCheckout.Store(true) // any checkout attempt fails, like the capped user

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()

	tx, err := beginTxOnConn(ctx, conn, "main")
	if err != nil {
		t.Fatalf("beginTxOnConn on the session's own branch: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := drv.countQuery("DOLT_CHECKOUT"); got != 0 {
		t.Fatalf("DOLT_CHECKOUT ran %d times, want 0 (session already on the requested branch)", got)
	}
	if got := drv.countQuery("active_branch"); got != 1 {
		t.Fatalf("active_branch ran %d times, want 1", got)
	}
}

// TestBeginTxOnConnChecksOutDifferentBranch pins that the fallback still
// switches a fresh session that is NOT on the requested branch.
func TestBeginTxOnConnChecksOutDifferentBranch(t *testing.T) {
	ctx := context.Background()
	db, drv := openMockDB(t)
	drv.activeBranch.Store("main")

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()

	tx, err := beginTxOnConn(ctx, conn, "feature-x")
	if err != nil {
		t.Fatalf("beginTxOnConn on a different branch: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := drv.countQuery("DOLT_CHECKOUT"); got != 1 {
		t.Fatalf("DOLT_CHECKOUT ran %d times, want 1 (session must be switched to the requested branch)", got)
	}
}

// TestBeginTxOnConnSurfacesActiveBranchError pins that a failed branch read is
// reported rather than papered over with a blind checkout.
func TestBeginTxOnConnSurfacesActiveBranchError(t *testing.T) {
	ctx := context.Background()
	db, drv := openMockDB(t)
	drv.failActiveBranch.Store(true)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()

	tx, err := beginTxOnConn(ctx, conn, "main")
	if err == nil {
		_ = tx.Rollback()
		t.Fatal("expected an error when active_branch() fails")
	}
	if !strings.Contains(err.Error(), "active branch") {
		t.Fatalf("error = %q, want it to name the active-branch read", err.Error())
	}
	if got := drv.countQuery("DOLT_CHECKOUT"); got != 0 {
		t.Fatalf("DOLT_CHECKOUT ran %d times, want 0", got)
	}
}

// readOnlyMigrationTables are the schema-version tables a least-privilege operator
// only reads: covered
// by the db-level SELECT, never granted DML.
var readOnlyMigrationTables = map[string]bool{
	"schema_migrations":         true,
	"ignored_schema_migrations": true,
}

// openCappedOperatorStore opens a store as a least-privilege operator user, as a
// shared Dolt server may grant one: SELECT on the database, DML on the data tables,
// and EXECUTE on only dolt_add/dolt_commit. The pool is pinned to one
// connection, so the ignored tx must take the fresh-dial fallback. The caller
// builds its own context after this returns (see openStoreWithOwnBudget).
func openCappedOperatorStore(t *testing.T) (admin, op *DoltStore) {
	t.Helper()
	admin, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup) // registered first so it runs after the DROP USER cleanup below

	user, password := grantCappedOperator(t, admin)
	op, err := openAsCappedOperator(t, admin.database, user, password)
	if err != nil {
		t.Fatalf("open store as capped operator: %v", err)
	}
	t.Cleanup(func() { _ = op.Close() })
	return admin, op
}

// grantCappedOperator creates the least-privilege operator user described on
// openCappedOperatorStore for admin's database and returns its credentials.
// The user is dropped when the test ends. dolt_* system tables (dolt_ignore
// among them) get no DML grant: only the database-wide SELECT covers them.
func grantCappedOperator(t *testing.T, admin *DoltStore) (user, password string) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()

	dbName := admin.database
	user = "capped_op_" + strings.TrimPrefix(dbName, "testdb_")
	password = "capped-pw"

	exec := func(stmt string) {
		t.Helper()
		if _, err := admin.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	exec(fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", user, password))
	t.Cleanup(func() {
		_, _ = admin.db.ExecContext(context.Background(), fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", user))
	})
	exec(fmt.Sprintf("GRANT SELECT ON `%s`.* TO '%s'@'%%'", dbName, user))

	rows, err := admin.db.QueryContext(ctx, "SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			_ = rows.Close()
			t.Fatalf("scan table: %v", err)
		}
		if !readOnlyMigrationTables[name] && !strings.HasPrefix(name, "dolt_") {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	_ = rows.Close()
	if len(tables) == 0 {
		t.Fatal("no data tables found to grant")
	}
	for _, table := range tables {
		exec(fmt.Sprintf("GRANT INSERT, UPDATE, DELETE ON `%s`.`%s` TO '%s'@'%%'", dbName, table, user))
	}
	for _, proc := range []string{"dolt_add", "dolt_commit"} {
		exec(fmt.Sprintf("GRANT EXECUTE ON PROCEDURE `%s`.`%s` TO '%s'@'%%'", dbName, proc, user))
	}
	return user, password
}

// openAsCappedOperator opens dbName with the credentials grantCappedOperator
// returned, pool pinned to one connection. The caller owns closing the store.
func openAsCappedOperator(t *testing.T, dbName, user, password string) (*DoltStore, error) {
	t.Helper()
	return openStoreWithOwnBudget(t, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		ServerUser:     user,
		ServerPassword: password,
		MaxOpenConns:   1,
	})
}

// TestIgnoredTxFallbackUnderCappedOperatorUser reproduces the failure end to
// end: writing a wisp in a transaction as a capped operator
// used to fail with "failed to checkout ignored tx branch main: ... command
// denied" (bd create --graph, bd mol wisp).
func TestIgnoredTxFallbackUnderCappedOperatorUser(t *testing.T) {
	admin, op := openCappedOperatorStore(t)
	ctx, cancel := testContext(t)
	defer cancel()

	wisp := &types.Issue{
		ID:        "test-capped-wisp",
		Title:     "wisp written by a capped operator",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := op.RunInTransaction(ctx, "test: capped operator writes a wisp", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, wisp, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction as capped operator: %v", err)
	}

	assertWispCount(ctx, t, admin.db, wisp.ID, 1)
}

// TestPinnedConnReadsUnderCappedOperatorUser covers the other fresh-connection
// site, pinStoreBranch: the long-timeout history read and the pinned-conn
// blocked recompute used to fail with "checkout active branch \"main\": ...
// command denied" for the same capped operator.
func TestPinnedConnReadsUnderCappedOperatorUser(t *testing.T) {
	_, op := openCappedOperatorStore(t)
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "test-capped-history",
		Title:     "durable issue read back by a capped operator",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := op.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue as capped operator: %v", err)
	}
	if _, err := op.RecomputeAllBlocked(ctx); err != nil {
		t.Fatalf("RecomputeAllBlocked as capped operator: %v", err)
	}
	if _, err := op.History(ctx, issue.ID); err != nil {
		t.Fatalf("History as capped operator: %v", err)
	}
}

// TestCappedOperatorOpensWithRefusedIgnoreSeed is the end-to-end form of
// GH#6169 (bd-obnpv0): a new canonical dolt_ignore pattern leaves every
// existing database under-seeded, and the least-privilege operator, which may
// not write dolt_ignore, used to fail every open on the denied seed INSERT
// until a privileged bd healed the pattern. On an otherwise-current schema
// with no new table that a missing pattern could leave versioned, the open
// must now succeed and leave the pattern for the privileged opener, whose
// existing heal path inserts and commits it. A pending new table keeps the
// refusal.
func TestCappedOperatorOpensWithRefusedIgnoreSeed(t *testing.T) {
	admin, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup) // registered first so it runs after the DROP USER cleanup

	const pattern = "__temp__%"
	const scratch = "bd_obnpv0_unignored_scratch"
	adminExec := func(stmt string, args ...any) {
		t.Helper()
		ctx, cancel := testContext(t)
		defer cancel()
		if _, err := admin.db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	patternRows := func(asOf string) int {
		t.Helper()
		ctx, cancel := testContext(t)
		defer cancel()
		var n int
		//nolint:gosec // G202: asOf is one of the test's own literals.
		query := "SELECT COUNT(*) FROM dolt_ignore" + asOf + " WHERE pattern = ?"
		if err := admin.db.QueryRowContext(ctx, query, pattern).Scan(&n); err != nil {
			t.Fatalf("read dolt_ignore%s: %v", asOf, err)
		}
		return n
	}

	// A database that predates the pattern: remove it and commit the removal,
	// so the working set is clean and only the pattern is missing.
	adminExec("DELETE FROM dolt_ignore WHERE pattern = ?", pattern)
	adminExec("CALL DOLT_ADD('dolt_ignore')")
	adminExec("CALL DOLT_COMMIT('-m', 'test: drop a canonical dolt_ignore pattern', '--skip-empty')")
	if got := patternRows(" AS OF 'HEAD'"); got != 0 {
		t.Fatalf("fixture: %q still committed at HEAD (%d rows)", pattern, got)
	}

	user, password := grantCappedOperator(t, admin)

	// A new, unignored table is what a missing pattern could leave versioned,
	// so the operator must still be refused while one is pending.
	adminExec("CREATE TABLE " + scratch + " (id INT PRIMARY KEY)")
	if op, err := openAsCappedOperator(t, admin.database, user, password); err == nil {
		_ = op.Close()
		t.Fatalf("capped operator opened a database whose dolt_status shows new table %s; want a refusal naming it", scratch)
	} else if !strings.Contains(err.Error(), scratch) || !strings.Contains(err.Error(), "dolt_ignore") {
		t.Fatalf("capped operator open error = %v, want it to name the seed denial and new table %s", err, scratch)
	}
	adminExec("DROP TABLE " + scratch)

	op, err := openAsCappedOperator(t, admin.database, user, password)
	if err != nil {
		t.Fatalf("capped operator open of an otherwise-current database missing %q: %v (a denied dolt_ignore seed must degrade to a warning)", pattern, err)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close capped operator store: %v", err)
	}
	if got := patternRows(""); got != 0 {
		t.Fatalf("capped operator open left %d %q rows in dolt_ignore, want 0 (it may not write dolt_ignore)", got, pattern)
	}

	healer, err := openStoreWithOwnBudget(t, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       admin.database,
	})
	if err != nil {
		t.Fatalf("privileged open after the degraded one: %v", err)
	}
	if err := healer.Close(); err != nil {
		t.Fatalf("close privileged store: %v", err)
	}
	if got := patternRows(""); got != 1 {
		t.Fatalf("privileged open left %d %q rows in the working set, want 1 (the heal)", got, pattern)
	}
	if got := patternRows(" AS OF 'HEAD'"); got != 1 {
		t.Fatalf("privileged open left %d %q rows at HEAD, want 1 (the heal must be committed)", got, pattern)
	}
	ctx, cancel := testContext(t)
	defer cancel()
	var dirty int
	if err := admin.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'dolt_ignore'").Scan(&dirty); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Fatalf("dolt_ignore still has %d dolt_status rows after the privileged heal, want 0", dirty)
	}
}
