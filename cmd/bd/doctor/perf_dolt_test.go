//go:build cgo

package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	embedded "github.com/dolthub/driver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// setupDoltWithCustomDBName creates a temporary dolt directory with a database
// named dbName and writes metadata.json referencing it. Returns the parent
// directory (containing .beads/).
func setupDoltWithCustomDBName(t *testing.T, dbName string) string {
	t.Helper()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	absPath, err := filepath.Abs(doltDir)
	if err != nil {
		t.Fatalf("failed to get abs path: %v", err)
	}

	// Create the database with the custom name
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
	_, err = initDB.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbName))
	if err != nil {
		_ = doltutil.CloseWithTimeout("initDB", initDB.Close)
		_ = doltutil.CloseWithTimeout("initConnector", initConnector.Close)
		t.Fatalf("failed to create database %q: %v", dbName, err)
	}

	// Create the issues table the diagnostics expect
	_, err = initDB.Exec(fmt.Sprintf("USE `%s`", dbName))
	if err != nil {
		_ = doltutil.CloseWithTimeout("initDB", initDB.Close)
		_ = doltutil.CloseWithTimeout("initConnector", initConnector.Close)
		t.Fatalf("failed to switch to database %q: %v", dbName, err)
	}

	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS issues (
			id VARCHAR(32) PRIMARY KEY,
			title TEXT,
			status VARCHAR(32) DEFAULT 'open',
			priority INT DEFAULT 2
		)`,
		`CREATE TABLE IF NOT EXISTS dependencies (
			issue_id VARCHAR(32),
			depends_on_id VARCHAR(32),
			PRIMARY KEY (issue_id, depends_on_id)
		)`,
		`CREATE TABLE IF NOT EXISTS labels (
			issue_id VARCHAR(32),
			label VARCHAR(64),
			PRIMARY KEY (issue_id, label)
		)`,
	} {
		if _, err := initDB.Exec(ddl); err != nil {
			_ = doltutil.CloseWithTimeout("initDB", initDB.Close)
			_ = doltutil.CloseWithTimeout("initConnector", initConnector.Close)
			t.Fatalf("failed to create table: %v", err)
		}
	}

	_ = doltutil.CloseWithTimeout("initDB", initDB.Close)
	_ = doltutil.CloseWithTimeout("initConnector", initConnector.Close)

	// Write metadata.json with the custom database name
	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_database":"%s"}`, dbName)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	return tmpDir
}

func TestRunDoltPerformanceDiagnostics_UsesConfiguredDBName(t *testing.T) {
	tmpDir := setupDoltWithCustomDBName(t, "beads_MyProject")

	metrics, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err != nil {
		t.Fatalf("expected diagnostics to succeed with custom db name, got error: %v", err)
	}

	if metrics.TotalIssues != 0 {
		t.Errorf("expected 0 issues in fresh db, got %d", metrics.TotalIssues)
	}
}

func TestRunDoltPerformanceDiagnostics_DefaultDBName(t *testing.T) {
	// When metadata.json has no dolt_database field, the default "beads" should be used
	tmpDir := setupDoltWithCustomDBName(t, "beads")

	// Overwrite metadata.json without dolt_database field
	beadsDir := filepath.Join(tmpDir, ".beads")
	metadata := `{"backend":"dolt"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	metrics, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err != nil {
		t.Fatalf("expected diagnostics to succeed with default db name, got error: %v", err)
	}

	if metrics.TotalIssues != 0 {
		t.Errorf("expected 0 issues in fresh db, got %d", metrics.TotalIssues)
	}
}

func TestCompareDoltModes_UsesConfiguredDBName(t *testing.T) {
	tmpDir := setupDoltWithCustomDBName(t, "beads_AnotherProject")

	// CompareDoltModes should not error when using a custom db name
	err := CompareDoltModes(tmpDir)
	if err != nil {
		t.Fatalf("expected CompareDoltModes to succeed with custom db name, got error: %v", err)
	}
}
