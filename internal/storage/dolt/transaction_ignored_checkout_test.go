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

// createCappedOperator creates a least-privilege operator user on admin's
// database, as a shared Dolt server may grant one: SELECT on the database,
// DML on the data tables, and EXECUTE on only dolt_add/dolt_commit. In
// particular the user can neither INSERT into dolt_ignore (a dolt_ table the
// grant loop skips) nor DOLT_CHECKOUT — the fence the seed-refusal tests
// reproduce. It returns the user's credentials; the caller opens its own
// store with them (see openStoreWithOwnBudget).
func createCappedOperator(t *testing.T, admin *DoltStore) (user, password string) {
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

// openCappedOperatorStore opens a store as a least-privilege operator user.
// The pool is pinned to one connection, so the ignored tx must take the
// fresh-dial fallback. The caller builds its own context after this returns
// (see openStoreWithOwnBudget).
func openCappedOperatorStore(t *testing.T) (admin, op *DoltStore) {
	t.Helper()
	admin, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup) // registered first so it runs after the DROP USER cleanup below

	user, password := createCappedOperator(t, admin)

	op, err := openStoreWithOwnBudget(t, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       admin.database,
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

// removeTempIgnorePatternFromHead drops the canonical __temp__ pattern from
// dolt_ignore and commits the removal, so any later store open owes the seed
// exactly one write — the shape a database has after a pattern was deleted
// out of band or an upgrade minted a new canonical pattern.
func removeTempIgnorePatternFromHead(t *testing.T, admin *DoltStore) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()

	exec := func(stmt string) {
		t.Helper()
		if _, err := admin.db.ExecContext(ctx, stmt); err != nil && !isDoltNothingToCommit(err) {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	exec("DELETE FROM dolt_ignore WHERE pattern = '__temp__%'")
	exec("CALL DOLT_ADD('dolt_ignore')")
	exec("CALL DOLT_COMMIT('-m', 'test: drop __temp__ ignore pattern')")
}

// TestCappedOperatorOpensWithRefusedIgnoreSeed reproduces GH#6169 end to end
// on a real server: a least-privilege operator (SELECT + DML-on-data +
// dolt_add/dolt_commit EXECUTE, no INSERT on dolt_ignore) opening a store
// whose dolt_ignore is missing a canonical pattern. The open used to die on
// the seed's denied INSERT; the approved design degrades it to a warning
// when the database has no migration work and no new table in the working
// set, and keeps it fatal when it does.
func TestCappedOperatorOpensWithRefusedIgnoreSeed(t *testing.T) {
	t.Run("operator opens degraded, privileged bd heals", func(t *testing.T) {
		admin, cleanup := setupConcurrentTestStore(t)
		t.Cleanup(cleanup)
		removeTempIgnorePatternFromHead(t, admin)

		user, password := createCappedOperator(t, admin)
		op, err := openStoreWithOwnBudget(t, &Config{
			Path:           t.TempDir(),
			CommitterName:  "test",
			CommitterEmail: "test@example.com",
			Database:       admin.database,
			ServerUser:     user,
			ServerPassword: password,
			MaxOpenConns:   1,
		})
		if err != nil {
			t.Fatalf("open store as capped operator with a refused dolt_ignore seed: %v", err)
		}
		t.Cleanup(func() { _ = op.Close() })

		ctx, cancel := testContext(t)
		defer cancel()

		// The degraded open continues without seeding: the pattern stays
		// unhealed (and unattempted) until a privileged open runs.
		var n int
		if err := op.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = '__temp__%'").Scan(&n); err != nil {
			t.Fatalf("read dolt_ignore as capped operator: %v", err)
		}
		if n != 0 {
			t.Fatalf("dolt_ignore holds __temp__ after a refused seed, want 0 rows")
		}

		// One privileged write-mode open heals the pattern — the remedy the
		// refusal names — and leaves the working set clean behind it.
		healed, err := openStoreWithOwnBudget(t, &Config{
			Path:           t.TempDir(),
			CommitterName:  "test",
			CommitterEmail: "test@example.com",
			Database:       admin.database,
			MaxOpenConns:   1,
		})
		if err != nil {
			t.Fatalf("privileged reopen after a degraded open: %v", err)
		}
		t.Cleanup(func() { _ = healed.Close() })

		if err := healed.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = '__temp__%'").Scan(&n); err != nil {
			t.Fatalf("read dolt_ignore as privileged bd: %v", err)
		}
		if n != 1 {
			t.Fatalf("privileged reopen healed __temp__ rows = %d, want 1", n)
		}
		if err := healed.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'dolt_ignore'").Scan(&n); err != nil {
			t.Fatalf("read dolt_status as privileged bd: %v", err)
		}
		if n != 0 {
			t.Fatalf("heal left dolt_ignore dirty in the working set, dolt_status rows = %d, want 0", n)
		}
	})

	t.Run("a new table in the working set refuses the degraded open", func(t *testing.T) {
		admin, cleanup := setupConcurrentTestStore(t)
		t.Cleanup(cleanup)
		removeTempIgnorePatternFromHead(t, admin)

		ctx, cancel := testContext(t)
		defer cancel()

		// An uncommitted table the missing pattern would have covered: a
		// later pass could capture it into HEAD.
		if _, err := admin.db.ExecContext(ctx, "CREATE TABLE `__temp__gh6169` (id INT PRIMARY KEY)"); err != nil {
			t.Fatalf("create __temp__gh6169: %v", err)
		}
		var n int
		if err := admin.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = '__temp__gh6169' AND status = 'new table'").Scan(&n); err != nil {
			t.Fatalf("read dolt_status: %v", err)
		}
		if n != 1 {
			t.Fatalf("setup sanity: __temp__gh6169 reported as new table %d times, want 1", n)
		}

		user, password := createCappedOperator(t, admin)
		_, err := openStoreWithOwnBudget(t, &Config{
			Path:           t.TempDir(),
			CommitterName:  "test",
			CommitterEmail: "test@example.com",
			Database:       admin.database,
			ServerUser:     user,
			ServerPassword: password,
			MaxOpenConns:   1,
		})
		if err == nil {
			t.Fatal("open as capped operator succeeded, want the capture-guard refusal")
		}
		for _, want := range []string{"__temp__gh6169", "open this database once with a privileged bd that can write dolt_ignore"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("open error = %q, want it to name %q", err.Error(), want)
			}
		}
	})
}
