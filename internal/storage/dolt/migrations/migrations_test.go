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

// openTestDolt creates a temporary Dolt database via sql-server for testing.
func openTestDolt(t *testing.T) *sql.DB {
	t.Helper()

	// Skip if dolt is not available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping test")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "testdb")
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		t.Fatalf("failed to create db dir: %v", err)
	}

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init")
	cmd.Dir = dbPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	// Start dolt sql-server on a random-ish port
	port := 13400 + os.Getpid()%100
	serverCmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
		"--no-auto-commit",
	)
	serverCmd.Dir = dbPath
	serverCmd.Stdout = os.Stderr
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		if serverCmd.Process != nil {
			_ = serverCmd.Process.Kill()
			_ = serverCmd.Wait()
		}
	})

	// Wait for server to be ready
	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/?parseTime=true", port)
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
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect to dolt sql-server: %v", err)
	}

	// Create database and schema
	if _, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads"); err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	if _, err := db.Exec("USE beads"); err != nil {
		t.Fatalf("failed to use database: %v", err)
	}

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

	// Reconnect with database in DSN
	_ = db.Close()
	dbDSN := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/beads?parseTime=true", port)
	db, err = sql.Open("mysql", dbDSN)
	if err != nil {
		t.Fatalf("failed to connect to beads database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

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
