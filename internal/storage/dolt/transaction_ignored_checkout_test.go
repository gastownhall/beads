package dolt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCheckoutBranchIfNeededSkipsCheckoutWhenAlreadyOnBranch pins that a
// fresh connection put on the store's branch does not issue DOLT_CHECKOUT when
// the session already sits there. A fresh session lands on the database's
// default branch, so this is the normal case — and a capped operator user that
// may EXECUTE only dolt_add/dolt_commit is denied DOLT_CHECKOUT outright.
func TestCheckoutBranchIfNeededSkipsCheckoutWhenAlreadyOnBranch(t *testing.T) {
	ctx := context.Background()
	db, drv := openMockDB(t)
	drv.failCheckout.Store(true) // any checkout attempt fails, like the capped user

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()

	if err := checkoutBranchIfNeeded(ctx, conn, "main"); err != nil {
		t.Fatalf("checkoutBranchIfNeeded on the session's own branch: %v", err)
	}
	if got := drv.countQuery("DOLT_CHECKOUT"); got != 0 {
		t.Fatalf("DOLT_CHECKOUT ran %d times, want 0 (session already on the requested branch)", got)
	}
	if got := drv.countQuery("active_branch"); got != 1 {
		t.Fatalf("active_branch ran %d times, want 1", got)
	}
}

// TestCheckoutBranchIfNeededChecksOutDifferentBranch pins that a fresh
// session that is NOT on the requested branch is still switched.
func TestCheckoutBranchIfNeededChecksOutDifferentBranch(t *testing.T) {
	ctx := context.Background()
	db, drv := openMockDB(t)
	drv.activeBranch.Store("main")

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()

	if err := checkoutBranchIfNeeded(ctx, conn, "feature-x"); err != nil {
		t.Fatalf("checkoutBranchIfNeeded on a different branch: %v", err)
	}
	if got := drv.countQuery("DOLT_CHECKOUT"); got != 1 {
		t.Fatalf("DOLT_CHECKOUT ran %d times, want 1 (session must be switched to the requested branch)", got)
	}
}

// TestCheckoutBranchIfNeededSurfacesActiveBranchError pins that a failed
// branch read is reported rather than papered over with a blind checkout.
func TestCheckoutBranchIfNeededSurfacesActiveBranchError(t *testing.T) {
	ctx := context.Background()
	db, drv := openMockDB(t)
	drv.failActiveBranch.Store(true)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()

	err = checkoutBranchIfNeeded(ctx, conn, "main")
	if err == nil {
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
// connection. The caller builds its own context after this returns (see
// openStoreWithOwnBudget).
func openCappedOperatorStore(t *testing.T) (admin, op *DoltStore) {
	t.Helper()
	admin, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup) // registered first so it runs after the DROP USER cleanup below

	ctx, cancel := testContext(t)
	defer cancel()

	dbName := admin.database
	user := "capped_op_" + strings.TrimPrefix(dbName, "testdb_")
	const password = "capped-pw"

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

	op, err = openStoreWithOwnBudget(t, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		ServerUser:     user,
		ServerPassword: password,
		MaxOpenConns:   1,
	})
	if err != nil {
		t.Fatalf("open store as capped operator: %v", err)
	}
	t.Cleanup(func() { _ = op.Close() })
	return admin, op
}

// TestIgnoredTxFallbackUnderCappedOperatorUser reproduces the failure end to
// end: writing a wisp in a transaction as a capped operator used to fail with
// "failed to checkout ignored tx branch main: ... command denied" (bd create
// --graph, bd mol wisp) when the wisp plane ran on a second, freshly dialed
// session. It now runs on the transaction's own pinned session; the test keeps
// a capped operator's wisp writes working.
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
