package migrations

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	// Initialize dolt repo
	initCmd := exec.Command("dolt", "init")
	initCmd.Dir = dbPath
	initCmd.Env = append(os.Environ(), "DOLT_ROOT_PATH="+tmpDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %v\n%s", err, out)
	}

	// Create beads database
	sqlCmd := exec.Command("dolt", "sql", "-q", "CREATE DATABASE IF NOT EXISTS beads")
	sqlCmd.Dir = dbPath
	sqlCmd.Env = append(os.Environ(), "DOLT_ROOT_PATH="+tmpDir)
	if out, err := sqlCmd.CombinedOutput(); err != nil {
		t.Fatalf("create database failed: %v\n%s", err, out)
	}

	// Find a free port
	port := 13307 + os.Getpid()%1000

	// Start dolt sql-server
	serverCmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
		"--user", "root",
		"--no-auto-commit",
	)
	serverCmd.Dir = dbPath
	serverCmd.Env = append(os.Environ(), "DOLT_ROOT_PATH="+tmpDir)
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})

	// Wait for server to be ready
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/beads", port)
	var db *sql.DB
	var err error
	for i := 0; i < 30; i++ {
		db, err = sql.Open("mysql", dsn)
		if err == nil {
			if pingErr := db.Ping(); pingErr == nil {
				break
			}
			_ = db.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect to dolt server: %v", err)
	}
	if pingErr := db.Ping(); pingErr != nil {
		t.Fatalf("dolt server not ready after retries: %v", pingErr)
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
