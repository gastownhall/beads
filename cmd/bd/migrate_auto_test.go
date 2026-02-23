//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// createTestSQLiteDB creates a minimal SQLite database with the beads schema
// and populates it with the given number of test issues.
func createTestSQLiteDB(t *testing.T, dbPath string, prefix string, issueCount int) {
	t.Helper()

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to create test SQLite DB: %v", err)
	}
	defer db.Close()

	// Create minimal schema matching what extractFromSQLite expects
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE IF NOT EXISTS issues (
			id TEXT PRIMARY KEY,
			content_hash TEXT DEFAULT '',
			title TEXT DEFAULT '',
			description TEXT DEFAULT '',
			design TEXT DEFAULT '',
			acceptance_criteria TEXT DEFAULT '',
			notes TEXT DEFAULT '',
			status TEXT DEFAULT 'open',
			priority INTEGER DEFAULT 2,
			issue_type TEXT DEFAULT 'task',
			assignee TEXT DEFAULT '',
			estimated_minutes INTEGER,
			created_at TEXT DEFAULT '',
			created_by TEXT DEFAULT '',
			owner TEXT DEFAULT '',
			updated_at TEXT DEFAULT '',
			closed_at TEXT,
			external_ref TEXT,
			compaction_level INTEGER DEFAULT 0,
			compacted_at TEXT DEFAULT '',
			compacted_at_commit TEXT,
			original_size INTEGER DEFAULT 0,
			sender TEXT DEFAULT '',
			ephemeral INTEGER DEFAULT 0,
			pinned INTEGER DEFAULT 0,
			is_template INTEGER DEFAULT 0,
			crystallizes INTEGER DEFAULT 0,
			mol_type TEXT DEFAULT '',
			work_type TEXT DEFAULT '',
			quality_score REAL,
			source_system TEXT DEFAULT '',
			source_repo TEXT DEFAULT '',
			close_reason TEXT DEFAULT '',
			event_kind TEXT DEFAULT '',
			actor TEXT DEFAULT '',
			target TEXT DEFAULT '',
			payload TEXT DEFAULT '',
			await_type TEXT DEFAULT '',
			await_id TEXT DEFAULT '',
			timeout_ns INTEGER DEFAULT 0,
			waiters TEXT DEFAULT '',
			hook_bead TEXT DEFAULT '',
			role_bead TEXT DEFAULT '',
			agent_state TEXT DEFAULT '',
			last_activity TEXT DEFAULT '',
			role_type TEXT DEFAULT '',
			rig TEXT DEFAULT '',
			due_at TEXT DEFAULT '',
			defer_until TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS labels (issue_id TEXT, label TEXT)`,
		`CREATE TABLE IF NOT EXISTS dependencies (
			issue_id TEXT, depends_on_id TEXT, type TEXT DEFAULT '',
			created_by TEXT DEFAULT '', created_at TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			issue_id TEXT, event_type TEXT DEFAULT '', actor TEXT DEFAULT '',
			old_value TEXT, new_value TEXT, comment TEXT, created_at TEXT DEFAULT ''
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create schema: %v", err)
		}
	}

	// Set prefix
	if prefix != "" {
		if _, err := db.Exec(`INSERT INTO config (key, value) VALUES ('issue_prefix', ?)`, prefix); err != nil {
			t.Fatalf("failed to set prefix: %v", err)
		}
	}

	// Insert test issues
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < issueCount; i++ {
		id := prefix + "-autotest-" + time.Now().Format("150405") + "-" + string(rune('a'+i))
		_, err := db.Exec(`INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at)
			VALUES (?, ?, 'open', 2, 'task', ?, ?)`,
			id, "Test issue "+id, now, now)
		if err != nil {
			t.Fatalf("failed to insert test issue: %v", err)
		}
		// Add a label
		if _, err := db.Exec(`INSERT INTO labels (issue_id, label) VALUES (?, 'test-label')`, id); err != nil {
			t.Fatalf("failed to insert label: %v", err)
		}
	}
}

func TestAutoMigrate_NoBeadsDir(t *testing.T) {
	// doAutoMigrateSQLiteToDolt should be a no-op for nonexistent dirs
	doAutoMigrateSQLiteToDolt("/nonexistent/path/.beads")
	// No panic or error = pass
}

func TestAutoMigrate_NoSQLiteDB(t *testing.T) {
	// .beads dir exists but has no .db files
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	doAutoMigrateSQLiteToDolt(beadsDir)
	// Should return without doing anything
}

func TestAutoMigrate_DoltAlreadyExists(t *testing.T) {
	// .beads has both beads.db and dolt/ — should rename beads.db
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(sqlitePath, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}

	doAutoMigrateSQLiteToDolt(beadsDir)

	// beads.db should be renamed to beads.db.migrated
	if _, err := os.Stat(sqlitePath); !os.IsNotExist(err) {
		t.Error("beads.db should have been renamed")
	}
	migratedPath := sqlitePath + ".migrated"
	if _, err := os.Stat(migratedPath); err != nil {
		t.Errorf("beads.db.migrated should exist: %v", err)
	}
}

func TestAutoMigrate_DoltExistsWithMigrated(t *testing.T) {
	// .beads has beads.db, beads.db.migrated, and dolt/ — should not rename again
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(sqlitePath, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	migratedPath := sqlitePath + ".migrated"
	if err := os.WriteFile(migratedPath, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	doAutoMigrateSQLiteToDolt(beadsDir)

	// Both files should still exist (no overwrite)
	if _, err := os.Stat(sqlitePath); err != nil {
		t.Error("beads.db should still exist (not renamed because .migrated already exists)")
	}
	if _, err := os.Stat(migratedPath); err != nil {
		t.Error("beads.db.migrated should still exist")
	}
}

func TestAutoMigrate_FullMigration(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write metadata.json with server config so migration can connect
	cfg := &configfile.Config{
		Database:       "beads.db",
		Backend:        "sqlite",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to write test metadata.json: %v", err)
	}

	// Create SQLite database with test data
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "mig", 3)

	// Run auto-migration
	doAutoMigrateSQLiteToDolt(beadsDir)

	// Verify: beads.db renamed
	if _, err := os.Stat(sqlitePath); !os.IsNotExist(err) {
		t.Error("beads.db should have been renamed to .migrated")
	}
	if _, err := os.Stat(sqlitePath + ".migrated"); err != nil {
		t.Errorf("beads.db.migrated should exist: %v", err)
	}

	// Verify: metadata.json updated
	updatedCfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to load updated config: %v", err)
	}
	if updatedCfg.Backend != configfile.BackendDolt {
		t.Errorf("backend should be 'dolt', got %q", updatedCfg.Backend)
	}
	if updatedCfg.Database != "dolt" {
		t.Errorf("database should be 'dolt', got %q", updatedCfg.Database)
	}
	if updatedCfg.DoltDatabase != "beads_mig" {
		t.Errorf("dolt_database should be 'beads_mig', got %q", updatedCfg.DoltDatabase)
	}

	// Verify: config.yaml has sync.mode
	configYaml := filepath.Join(beadsDir, "config.yaml")
	if data, err := os.ReadFile(configYaml); err == nil {
		if !strings.Contains(string(data), "dolt-native") {
			t.Error("config.yaml should contain sync.mode = dolt-native")
		}
	}

	// Clean up Dolt test database
	dropTestDatabase("beads_mig", testDoltServerPort)
}

func TestAutoMigrate_CorruptedSQLite(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a corrupt file as beads.db
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(sqlitePath, []byte("this is not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}

	// Should warn but not panic
	doAutoMigrateSQLiteToDolt(beadsDir)

	// beads.db should still exist (not renamed since migration failed)
	if _, err := os.Stat(sqlitePath); err != nil {
		t.Error("beads.db should still exist after failed migration")
	}
	// dolt/ should not exist
	if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(err) {
		t.Error("dolt/ should not exist after failed migration")
	}
}

func TestAutoMigrate_ExtractFromSQLite(t *testing.T) {
	// Test that extractFromSQLite correctly reads test data
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "ext", 5)

	ctx := t.Context()
	data, err := extractFromSQLite(ctx, sqlitePath)
	if err != nil {
		t.Fatalf("extractFromSQLite failed: %v", err)
	}

	if data.prefix != "ext" {
		t.Errorf("expected prefix 'ext', got %q", data.prefix)
	}
	if data.issueCount != 5 {
		t.Errorf("expected 5 issues, got %d", data.issueCount)
	}
	if len(data.issues) != 5 {
		t.Errorf("expected 5 issues in slice, got %d", len(data.issues))
	}

	// Verify labels were loaded
	hasLabels := false
	for _, issue := range data.issues {
		if len(issue.Labels) > 0 {
			hasLabels = true
			break
		}
	}
	if !hasLabels {
		t.Error("expected at least one issue to have labels")
	}

	// Verify config was loaded
	if data.config["issue_prefix"] != "ext" {
		t.Errorf("config should contain issue_prefix=ext, got %v", data.config)
	}
}

func TestAutoMigrate_Idempotent(t *testing.T) {
	// Calling doAutoMigrateSQLiteToDolt twice should be safe
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No SQLite DB — should be no-op both times
	doAutoMigrateSQLiteToDolt(beadsDir)
	doAutoMigrateSQLiteToDolt(beadsDir)
}

// Verify the migrated metadata.json is valid JSON
func TestAutoMigrate_MetadataJSONValid(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write initial metadata.json
	cfg := &configfile.Config{
		Database:       "beads.db",
		Backend:        "sqlite",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "json", 1)

	doAutoMigrateSQLiteToDolt(beadsDir)

	// Read and parse metadata.json
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Errorf("metadata.json is not valid JSON: %v\nContent: %s", err, string(data))
	}

	// Clean up
	dropTestDatabase("beads_json", testDoltServerPort)
}
