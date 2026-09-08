package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver for direct DB connections
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// branchPrefix is the prefix for all test branches, used for cleanup.
const branchPrefix = "test-"

// maxTestNameLen is the max length of the sanitized test name in a branch name.
const maxTestNameLen = 40

// sanitizeTestName converts a test name to a branch-safe string.
// Only alphanumeric, dash, and underscore are kept; slashes become dashes.
func sanitizeTestName(name string) string {
	// Replace slashes with dashes (subtests use /)
	name = strings.ReplaceAll(name, "/", "-")
	// Keep only safe chars
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	name = re.ReplaceAllString(name, "")
	// Truncate
	if len(name) > maxTestNameLen {
		name = name[:maxTestNameLen]
	}
	return name
}

// StartTestBranch creates an isolated Dolt branch for a single test.
// It creates a branch from HEAD of the given database, checks it out on the
// current connection, and returns the branch name and a cleanup function.
//
// IMPORTANT: The db must have MaxOpenConns(1) to ensure DOLT_CHECKOUT
// affects all queries (it's session-level, and each pooled connection
// is a separate session).
//
// The cleanup function switches back to main and deletes the branch.
func StartTestBranch(t *testing.T, db *sql.DB, database string) (branchName string, cleanup func()) {
	t.Helper()

	// Generate unique branch name
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("StartTestBranch: failed to generate random bytes: %v", err)
	}
	branchName = branchPrefix + sanitizeTestName(t.Name()) + "-" + hex.EncodeToString(buf)

	// USE the correct database before branch operations
	//nolint:gosec // G201: database name comes from test infrastructure
	if _, err := db.Exec(fmt.Sprintf("USE `%s`", database)); err != nil {
		t.Fatalf("StartTestBranch: USE %s failed: %v", database, err)
	}

	// Create branch from HEAD
	if _, err := db.Exec("CALL DOLT_BRANCH(?, 'main')", branchName); err != nil {
		t.Fatalf("StartTestBranch: DOLT_BRANCH(%s) failed: %v", branchName, err)
	}

	// Checkout the branch
	if _, err := db.Exec("CALL DOLT_CHECKOUT(?)", branchName); err != nil {
		// Clean up the branch we just created
		_, _ = db.Exec("CALL DOLT_BRANCH('-D', ?)", branchName)
		t.Fatalf("StartTestBranch: DOLT_CHECKOUT(%s) failed: %v", branchName, err)
	}

	cleanup = func() {
		// Switch back to main before deleting
		_, _ = db.Exec(fmt.Sprintf("USE `%s`", database))
		_, _ = db.Exec("CALL DOLT_CHECKOUT('main')")
		_, _ = db.Exec("CALL DOLT_BRANCH('-D', ?)", branchName)
	}

	return branchName, cleanup
}

// ResetTestBranch resets the current branch's working set back to HEAD,
// discarding all uncommitted changes. Use this in table-driven tests to
// restore seed data state between subtests, avoiding the overhead of
// creating a new branch for each subtest.
//
// Usage pattern:
//
//	branch, cleanup := StartTestBranch(t, db, database)
//	// ... seed data, then DOLT_COMMIT to set HEAD ...
//	for _, tc := range testCases {
//	    // ... run test case that modifies data ...
//	    ResetTestBranch(t, db) // reset to seed state
//	}
func ResetTestBranch(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("CALL DOLT_RESET('--hard')"); err != nil {
		t.Fatalf("ResetTestBranch: DOLT_RESET('--hard') failed: %v", err)
	}
}

// CleanTestBranches removes stale test branches left by crashed tests.
// Call this in TestMain after creating the shared database.
func CleanTestBranches(db *sql.DB, database string) {
	//nolint:gosec // G201: database name comes from test infrastructure
	_, _ = db.Exec(fmt.Sprintf("USE `%s`", database))

	rows, err := db.Query("SELECT name FROM dolt_branches WHERE name LIKE 'test-%'")
	if err != nil {
		return
	}
	defer rows.Close()

	var branches []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			branches = append(branches, name)
		}
	}

	// Make sure we're on main before deleting branches
	_, _ = db.Exec("CALL DOLT_CHECKOUT('main')")
	for _, branch := range branches {
		_, _ = db.Exec("CALL DOLT_BRANCH('-D', ?)", branch)
	}
}

// SetupSharedTestDB creates a shared database on the test Dolt server with
// committed schema for branch-per-test isolation. Call from TestMain after
// EnsureDoltContainerForTestMain. Returns the database name and a raw *sql.DB
// handle for branch cleanup.
//
// The schema is committed to main so that DOLT_BRANCH creates COW snapshots
// of the full schema. Each test then branches from main with StartTestBranch.
func SetupSharedTestDB(port int, dbName string) (*sql.DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Open a connection without selecting a database
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Timeout: 10 * time.Second}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("SetupSharedTestDB: open connection: %w", err)
	}

	// FIREWALL: refuse to create databases on the production port.
	if port == 3307 {
		_ = db.Close()
		return nil, fmt.Errorf("SetupSharedTestDB: REFUSED — port %d is production (Clown Shows #12-#18)", port)
	}

	// Create the shared database
	//nolint:gosec // G201: dbName comes from test infrastructure
	_, err = db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbName))
	if err != nil {
		// Dolt may return error 1007 even with IF NOT EXISTS
		errLower := strings.ToLower(err.Error())
		if !strings.Contains(errLower, "database exists") && !strings.Contains(errLower, "1007") {
			_ = db.Close()
			return nil, fmt.Errorf("SetupSharedTestDB: create database: %w", err)
		}
	}

	// Switch to the database and clean stale branches
	CleanTestBranches(db, dbName)

	return db, nil
}

type doltIgnoreRow struct {
	pattern string
	ignored bool
}

type doltBranchSQL interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

// MaterializeLocalTableSchemasForBranchTests makes the shared test database
// branchable without running local-table DDL on every test branch. Production
// opens always have these tables available after migration; branch-per-test
// isolation needs the same invariant in main before individual tests fork. The
// sequence is pinned to one Dolt SQL session because checkout, staging, commit,
// and dolt_ignore state are session-scoped.
func MaterializeLocalTableSchemasForBranchTests(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire materialization connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return fmt.Errorf("checkout main: %w", err)
	}

	ignoreRows, err := doltIgnoreRows(ctx, conn)
	if err != nil {
		return fmt.Errorf("read dolt_ignore rows: %w", err)
	}
	tableNames, err := ignoredTableNames(ctx, conn)
	if err != nil {
		return fmt.Errorf("read ignored table names: %w", err)
	}

	if err := removeDoltIgnoreRows(ctx, conn, ignoreRows); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('dolt_ignore')"); err != nil {
		return fmt.Errorf("stage unignored local table patterns: %w", err)
	}
	if err := commitAllowEmpty(ctx, conn, "test: unignore local table schemas"); err != nil {
		return err
	}

	for _, table := range tableNames {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD(?)", table); err != nil {
			return fmt.Errorf("stage local table %s: %w", table, err)
		}
	}
	if err := commitAllowEmpty(ctx, conn, "test: materialize local table schemas"); err != nil {
		return err
	}

	if err := restoreDoltIgnoreRows(ctx, conn, ignoreRows); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('dolt_ignore')"); err != nil {
		return fmt.Errorf("stage restored local table patterns: %w", err)
	}
	if err := commitAllowEmpty(ctx, conn, "test: restore local table ignores"); err != nil {
		return err
	}

	return nil
}

func doltIgnoreRows(ctx context.Context, db doltBranchSQL) ([]doltIgnoreRow, error) {
	rows, err := db.QueryContext(ctx, "SELECT pattern, ignored FROM dolt_ignore ORDER BY pattern")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []doltIgnoreRow
	for rows.Next() {
		var row doltIgnoreRow
		if err := rows.Scan(&row.pattern, &row.ignored); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func ignoredTableNames(ctx context.Context, db doltBranchSQL) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT t.table_name
		FROM information_schema.tables t
		JOIN dolt_ignore di ON di.ignored = true
		WHERE t.table_schema = DATABASE()
			AND (t.table_name = di.pattern OR t.table_name LIKE di.pattern)
		ORDER BY t.table_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		result = append(result, table)
	}
	return result, rows.Err()
}

func removeDoltIgnoreRows(ctx context.Context, db doltBranchSQL, rows []doltIgnoreRow) error {
	for _, row := range rows {
		if _, err := db.ExecContext(ctx, "DELETE FROM dolt_ignore WHERE pattern = ?", row.pattern); err != nil {
			return fmt.Errorf("remove dolt_ignore pattern %s: %w", row.pattern, err)
		}
	}
	return nil
}

func restoreDoltIgnoreRows(ctx context.Context, db doltBranchSQL, rows []doltIgnoreRow) error {
	for _, row := range rows {
		if _, err := db.ExecContext(ctx, "REPLACE INTO dolt_ignore (pattern, ignored) VALUES (?, ?)", row.pattern, row.ignored); err != nil {
			return fmt.Errorf("restore dolt_ignore pattern %s: %w", row.pattern, err)
		}
	}
	return nil
}

func commitAllowEmpty(ctx context.Context, db doltBranchSQL, message string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-m', ?)", message); err != nil {
		return fmt.Errorf("commit %q: %w", message, err)
	}
	return nil
}
