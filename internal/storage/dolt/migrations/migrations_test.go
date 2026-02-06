//go:build cgo
package migrations

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	embedded "github.com/dolthub/driver"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// openTestDolt creates a temporary embedded Dolt database for testing.
func openTestDolt(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "testdb")
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		t.Fatalf("failed to create db dir: %v", err)
	}

	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		t.Fatalf("failed to get abs path: %v", err)
	}

	// First connect without database to create it
	initDSN := fmt.Sprintf("file://%s?commitname=test&commitemail=test@test.com", absPath)
	initCfg, err := embedded.ParseDSN(initDSN)
	if err != nil {
		t.Fatalf("failed to parse init DSN: %v", err)
	}

	initConnector, err := embedded.NewConnector(initCfg)
	if err != nil {
		t.Fatalf("failed to create init connector: %v", err)
	}

	initDB := sql.OpenDB(initConnector)
	_, err = initDB.Exec("CREATE DATABASE IF NOT EXISTS beads")
	if err != nil {
		_ = doltutil.CloseWithTimeout("initDB", initDB.Close)
		_ = doltutil.CloseWithTimeout("initConnector", initConnector.Close)
		t.Fatalf("failed to create database: %v", err)
	}
	_ = doltutil.CloseWithTimeout("initDB", initDB.Close)
	_ = doltutil.CloseWithTimeout("initConnector", initConnector.Close)

	// Now connect with database specified
	dsn := fmt.Sprintf("file://%s?commitname=test&commitemail=test@test.com&database=beads", absPath)
	cfg, err := embedded.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("failed to parse DSN: %v", err)
	}

	connector, err := embedded.NewConnector(cfg)
	if err != nil {
		t.Fatalf("failed to create connector: %v", err)
	}
	t.Cleanup(func() { _ = doltutil.CloseWithTimeout("connector", connector.Close) })

	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = doltutil.CloseWithTimeout("db", db.Close) })

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


