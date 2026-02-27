package migrations

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// openTestDolt starts a temporary dolt sql-server and returns a connection.
func openTestDolt(t *testing.T) *sql.DB {
	t.Helper()

	// Check that dolt binary is available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt binary not found, skipping migration test")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "testdb")
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		t.Fatalf("failed to create db dir: %v", err)
	}

	// Filter DOLT_ROOT_PASSWORD to prevent auth interference in Doppler environments.
	// When DOLT_ROOT_PASSWORD is set, Dolt enables authentication and requires
	// credentials in the DSN, causing test failures with "root@tcp(...)" connections.
	var filteredEnv []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "DOLT_ROOT_PASSWORD=") {
			continue
		}
		filteredEnv = append(filteredEnv, e)
	}
	doltEnv := append(filteredEnv, "DOLT_ROOT_PATH="+tmpDir)
	for _, cfg := range []struct{ key, val string }{
		{"user.name", "Test User"},
		{"user.email", "test@example.com"},
	} {
		cfgCmd := exec.Command("dolt", "config", "--global", "--add", cfg.key, cfg.val)
		cfgCmd.Env = doltEnv
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt config %s failed: %v\n%s", cfg.key, err, out)
		}
	}

	// Initialize dolt repo
	initCmd := exec.Command("dolt", "init")
	initCmd.Dir = dbPath
	initCmd.Env = doltEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %v\n%s", err, out)
	}

	// Create beads database
	sqlCmd := exec.Command("dolt", "sql", "-q", "CREATE DATABASE IF NOT EXISTS beads")
	sqlCmd.Dir = dbPath
	sqlCmd.Env = doltEnv
	if out, err := sqlCmd.CombinedOutput(); err != nil {
		t.Fatalf("create database failed: %v\n%s", err, out)
	}

	// Find a free port by binding and releasing
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	// Start dolt sql-server
	serverCmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
	)
	serverCmd.Dir = dbPath
	serverCmd.Env = doltEnv
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})

	// Wait for server to be ready
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/beads?allowCleartextPasswords=true&allowNativePasswords=true", port)
	var db *sql.DB
	var lastPingErr error
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		db, err = sql.Open("mysql", dsn)
		if err != nil {
			continue
		}
		if pingErr := db.Ping(); pingErr == nil {
			lastPingErr = nil
			break
		} else {
			lastPingErr = pingErr
		}
		_ = db.Close()
		db = nil
	}
	if db == nil {
		t.Fatalf("dolt server not ready after retries: %v", lastPingErr)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Create minimal issues table without wisp_type (simulating old schema)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS issues (
		id VARCHAR(255) PRIMARY KEY,
		title VARCHAR(500) NOT NULL,
		status VARCHAR(32) NOT NULL DEFAULT 'open',
		ephemeral TINYINT(1) DEFAULT 0,
		pinned TINYINT(1) DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("failed to create issues table: %v", err)
	}

	return db
}

func TestMigrateWispTypeColumn(t *testing.T) {
	db := openTestDolt(t)

	// Verify column doesn't exist yet
	exists, err := columnExists(db, "issues", "wisp_type")
	if err != nil {
		t.Fatalf("failed to check column: %v", err)
	}
	if exists {
		t.Fatal("wisp_type should not exist yet")
	}

	// Run migration
	if err := MigrateWispTypeColumn(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify column now exists
	exists, err = columnExists(db, "issues", "wisp_type")
	if err != nil {
		t.Fatalf("failed to check column: %v", err)
	}
	if !exists {
		t.Fatal("wisp_type should exist after migration")
	}

	// Run migration again (idempotent)
	if err := MigrateWispTypeColumn(db); err != nil {
		t.Fatalf("re-running migration should be idempotent: %v", err)
	}
}

func TestColumnExists(t *testing.T) {
	db := openTestDolt(t)

	exists, err := columnExists(db, "issues", "id")
	if err != nil {
		t.Fatalf("failed to check column: %v", err)
	}
	if !exists {
		t.Fatal("id column should exist")
	}

	exists, err = columnExists(db, "issues", "nonexistent")
	if err != nil {
		t.Fatalf("failed to check column: %v", err)
	}
	if exists {
		t.Fatal("nonexistent column should not exist")
	}
}

func TestTableExists(t *testing.T) {
	db := openTestDolt(t)

	exists, err := tableExists(db, "issues")
	if err != nil {
		t.Fatalf("failed to check table: %v", err)
	}
	if !exists {
		t.Fatal("issues table should exist")
	}

	exists, err = tableExists(db, "nonexistent")
	if err != nil {
		t.Fatalf("failed to check table: %v", err)
	}
	if exists {
		t.Fatal("nonexistent table should not exist")
	}
}

func TestDetectOrphanedChildren(t *testing.T) {
	db := openTestDolt(t)

	// No orphans in empty database
	if err := DetectOrphanedChildren(db); err != nil {
		t.Fatalf("orphan detection failed on empty db: %v", err)
	}

	// Insert a parent and its child — no orphans
	_, err := db.Exec(`INSERT INTO issues (id, title, status) VALUES ('bd-parent1', 'Parent', 'open')`)
	if err != nil {
		t.Fatalf("failed to insert parent: %v", err)
	}
	_, err = db.Exec(`INSERT INTO issues (id, title, status) VALUES ('bd-parent1.1', 'Child 1', 'open')`)
	if err != nil {
		t.Fatalf("failed to insert child: %v", err)
	}

	if err := DetectOrphanedChildren(db); err != nil {
		t.Fatalf("orphan detection failed with valid parent-child: %v", err)
	}

	// Insert an orphan (child whose parent doesn't exist)
	_, err = db.Exec(`INSERT INTO issues (id, title, status) VALUES ('bd-missing.2', 'Orphan Child', 'open')`)
	if err != nil {
		t.Fatalf("failed to insert orphan: %v", err)
	}

	// Should succeed (logs orphans but doesn't error)
	if err := DetectOrphanedChildren(db); err != nil {
		t.Fatalf("orphan detection should not error on orphans: %v", err)
	}

	// Insert a deeply nested orphan (parent of intermediate level missing)
	_, err = db.Exec(`INSERT INTO issues (id, title, status) VALUES ('bd-gone.1.3', 'Deep Orphan', 'closed')`)
	if err != nil {
		t.Fatalf("failed to insert deep orphan: %v", err)
	}

	if err := DetectOrphanedChildren(db); err != nil {
		t.Fatalf("orphan detection should not error on deep orphans: %v", err)
	}

	// Idempotent — running again should be fine
	if err := DetectOrphanedChildren(db); err != nil {
		t.Fatalf("orphan detection should be idempotent: %v", err)
	}
}

func TestMigrateWispsTable(t *testing.T) {
	db := openTestDolt(t)

	// Verify wisps table doesn't exist yet
	exists, err := tableExists(db, "wisps")
	if err != nil {
		t.Fatalf("failed to check table: %v", err)
	}
	if exists {
		t.Fatal("wisps table should not exist yet")
	}

	// Run migration
	if err := MigrateWispsTable(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify wisps table now exists
	exists, err = tableExists(db, "wisps")
	if err != nil {
		t.Fatalf("failed to check table after migration: %v", err)
	}
	if !exists {
		t.Fatal("wisps table should exist after migration")
	}

	// Verify dolt_ignore has the patterns
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM dolt_ignore WHERE pattern IN ('wisps', 'wisp_%')").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query dolt_ignore: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 dolt_ignore patterns, got %d", count)
	}

	// Verify dolt_add('-A') does NOT stage the wisps table (dolt_ignore effect)
	_, err = db.Exec("CALL DOLT_ADD('-A')")
	if err != nil {
		t.Fatalf("dolt_add failed: %v", err)
	}

	// After dolt_add('-A'), wisps should remain unstaged due to dolt_ignore.
	var staged bool
	err = db.QueryRow("SELECT staged FROM dolt_status WHERE table_name = 'wisps'").Scan(&staged)
	if err == nil && staged {
		t.Fatal("wisps table should NOT be staged after dolt_add('-A') (dolt_ignore should prevent staging)")
	}

	// Run migration again (idempotent)
	if err := MigrateWispsTable(db); err != nil {
		t.Fatalf("re-running migration should be idempotent: %v", err)
	}

	// Verify we can INSERT and query from wisps table
	_, err = db.Exec(`INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes)
		VALUES ('wisp-test1', 'Test Wisp', 'desc', '', '', '')`)
	if err != nil {
		t.Fatalf("failed to insert into wisps: %v", err)
	}

	var title string
	err = db.QueryRow("SELECT title FROM wisps WHERE id = 'wisp-test1'").Scan(&title)
	if err != nil {
		t.Fatalf("failed to query wisps: %v", err)
	}
	if title != "Test Wisp" {
		t.Fatalf("expected title 'Test Wisp', got %q", title)
	}
}

func TestMigrateIssueCounterTable(t *testing.T) {
	db := openTestDolt(t)

	// Verify issue_counter table does not exist yet
	exists, err := tableExists(db, "issue_counter")
	if err != nil {
		t.Fatalf("failed to check table: %v", err)
	}
	if exists {
		t.Fatal("issue_counter should not exist yet")
	}

	// Run migration
	if err := MigrateIssueCounterTable(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify issue_counter table now exists
	exists, err = tableExists(db, "issue_counter")
	if err != nil {
		t.Fatalf("failed to check table after migration: %v", err)
	}
	if !exists {
		t.Fatal("issue_counter should exist after migration")
	}

	// Run migration again (idempotent)
	if err := MigrateIssueCounterTable(db); err != nil {
		t.Fatalf("re-running migration should be idempotent: %v", err)
	}

	// Verify we can INSERT and query from issue_counter
	_, err = db.Exec("INSERT INTO issue_counter (prefix, last_id) VALUES ('bd', 5)")
	if err != nil {
		t.Fatalf("failed to insert into issue_counter: %v", err)
	}

	var lastID int
	err = db.QueryRow("SELECT last_id FROM issue_counter WHERE prefix = 'bd'").Scan(&lastID)
	if err != nil {
		t.Fatalf("failed to query issue_counter: %v", err)
	}
	if lastID != 5 {
		t.Errorf("expected last_id 5, got %d", lastID)
	}
}

func TestColumnExistsNoTable(t *testing.T) {
	db := openTestDolt(t)

	// columnExists on a nonexistent table should return (false, nil),
	// not propagate the Error 1146 from SHOW COLUMNS.
	exists, err := columnExists(db, "nonexistent_table", "id")
	if err != nil {
		t.Fatalf("columnExists on nonexistent table should not error, got: %v", err)
	}
	if exists {
		t.Fatal("columnExists on nonexistent table should return false")
	}
}

func TestColumnExistsWithPhantom(t *testing.T) {
	db := openTestDolt(t)

	// Create a phantom-like database entry (simulates naming convention phantom)
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_phantom")
	if err != nil {
		t.Fatalf("failed to create phantom database: %v", err)
	}

	// Positive: still finds columns in primary database
	exists, err := columnExists(db, "issues", "id")
	if err != nil {
		t.Fatalf("columnExists failed with phantom present: %v", err)
	}
	if !exists {
		t.Fatal("should find 'id' column even with phantom database present")
	}

	// Positive: still finds tables
	exists, err = tableExists(db, "issues")
	if err != nil {
		t.Fatalf("tableExists failed with phantom present: %v", err)
	}
	if !exists {
		t.Fatal("should find 'issues' table even with phantom database present")
	}

	// Negative: missing column still returns false
	exists, err = columnExists(db, "issues", "nonexistent")
	if err != nil {
		t.Fatalf("should not error for missing column: %v", err)
	}
	if exists {
		t.Fatal("should return false for nonexistent column")
	}

	// Negative: missing table still returns (false, nil)
	exists, err = tableExists(db, "nonexistent_table")
	if err != nil {
		t.Fatalf("should not error for missing table: %v", err)
	}
	if exists {
		t.Fatal("should return false for nonexistent table")
	}

	// Negative: nonexistent table + column returns (false, nil)
	exists, err = columnExists(db, "nonexistent_table", "id")
	if err != nil {
		t.Fatalf("should not error with phantom database present: %v", err)
	}
	if exists {
		t.Fatal("should return false for column in nonexistent table")
	}
}

func TestMigrateInfraToWisps_SchemaEvolution(t *testing.T) {
	db := openTestDolt(t)
	defer db.Close()

	// 1. Create older issues table WITH a column that wisps won't have (deleted_at)
	// and WITHOUT a column that wisps will have (metadata)
	db.Exec("DROP TABLE IF EXISTS issues")
	_, err := db.Exec(`
		CREATE TABLE issues (
			id VARCHAR(255) PRIMARY KEY,
			title VARCHAR(500) NOT NULL,
			issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create issues table: %v", err)
	}

	// Insert an infra issue
	_, err = db.Exec("INSERT INTO issues (id, title, issue_type) VALUES ('test-1', 'Agent Wisp', 'agent')")
	if err != nil {
		t.Fatalf("Failed to insert issue: %v", err)
	}

	// 2. Create older dependencies table missing a column (thread_id)
	_, err = db.Exec(`
		CREATE TABLE dependencies (
			issue_id VARCHAR(255) NOT NULL,
			depends_on_id VARCHAR(255) NOT NULL,
			type VARCHAR(32) NOT NULL DEFAULT 'blocks',
			PRIMARY KEY (issue_id, depends_on_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create dependencies table: %v", err)
	}

	// Insert a dependency
	_, err = db.Exec("INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('test-1', 'test-2', 'blocks')")
	if err != nil {
		t.Fatalf("Failed to insert dependency: %v", err)
	}

	// 3. Create missing minimal tables for other relations so copyCommonColumns works
	_, err = db.Exec(`CREATE TABLE labels (issue_id VARCHAR(255), label VARCHAR(255))`)
	if err != nil { t.Fatal(err) }
	_, err = db.Exec(`CREATE TABLE events (id BIGINT AUTO_INCREMENT PRIMARY KEY, issue_id VARCHAR(255), event_type VARCHAR(32))`)
	if err != nil { t.Fatal(err) }
	_, err = db.Exec(`CREATE TABLE comments (id BIGINT AUTO_INCREMENT PRIMARY KEY, issue_id VARCHAR(255), text TEXT)`)
	if err != nil { t.Fatal(err) }

	// 4. Run migration 004 to create wisps table and 005 for auxiliary tables
	if err := MigrateWispsTable(db); err != nil {
		t.Fatalf("Failed to run migration 004: %v", err)
	}
	if err := MigrateWispAuxiliaryTables(db); err != nil {
		t.Fatalf("Failed to run migration 005: %v", err)
	}

	// 5. Run migration 007 - it should gracefully map columns instead of crashing
	if err := MigrateInfraToWisps(db); err != nil {
		t.Fatalf("Migration 007 failed: %v", err)
	}

	// 6. Verify row was moved
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = 'test-1'").Scan(&count); err != nil {
		t.Fatalf("Failed to query wisps: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 wisp, got %d", count)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM issues WHERE id = 'test-1'").Scan(&count); err != nil {
		t.Fatalf("Failed to query issues: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
}
