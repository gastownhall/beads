package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// --- backupSQLite tests ---

func TestBackupSQLite_CounterOverflow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beads.db")
	if err := os.WriteFile(dbPath, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create the base backup and all counter variants 1-100
	// to exhaust the counter loop
	first, err := backupSQLite(dbPath)
	if err != nil {
		t.Fatalf("first backup failed: %v", err)
	}
	// Extract timestamp from first backup name
	base := filepath.Base(first)
	// Pattern: beads.backup-pre-dolt-YYYYMMDD-HHMMSS.db
	parts := strings.SplitN(base, "backup-pre-dolt-", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected backup name: %s", base)
	}
	timestamp := strings.TrimSuffix(parts[1], ".db")

	// Create counter files 1-100
	for i := 1; i <= 100; i++ {
		name := fmt.Sprintf("beads.backup-pre-dolt-%s-%d.db", timestamp, i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Next backup attempt should fail with "too many backup files"
	_, err = backupSQLite(dbPath)
	if err == nil {
		t.Fatal("expected error when all counter slots are taken")
	}
	if !strings.Contains(err.Error(), "too many backup files") {
		t.Errorf("expected 'too many backup files' error, got: %v", err)
	}
}

func TestBackupSQLite_NoExtension(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beadsdb") // no extension
	if err := os.WriteFile(dbPath, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}

	backupPath, err := backupSQLite(dbPath)
	if err != nil {
		t.Fatalf("backupSQLite failed: %v", err)
	}

	if !strings.Contains(filepath.Base(backupPath), "backup-pre-dolt-") {
		t.Errorf("backup name %q missing expected pattern", filepath.Base(backupPath))
	}

	got, _ := os.ReadFile(backupPath)
	if string(got) != "data" {
		t.Errorf("backup content = %q, want %q", got, "data")
	}
}

func TestBackupSQLite_CreatesBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beads.db")
	content := []byte("sqlite-test-data")
	if err := os.WriteFile(dbPath, content, 0600); err != nil {
		t.Fatal(err)
	}

	backupPath, err := backupSQLite(dbPath)
	if err != nil {
		t.Fatalf("backupSQLite failed: %v", err)
	}

	// Original still exists.
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("original file missing after backup: %v", err)
	}

	// Backup exists and has correct content.
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("backup content = %q, want %q", got, content)
	}

	// Backup name contains expected pattern.
	if !strings.Contains(filepath.Base(backupPath), "backup-pre-dolt-") {
		t.Errorf("backup name %q missing expected pattern", filepath.Base(backupPath))
	}
}

func TestBackupSQLite_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent.db")

	_, err := backupSQLite(dbPath)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestBackupSQLite_BackupAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beads.db")
	if err := os.WriteFile(dbPath, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create first backup.
	first, err := backupSQLite(dbPath)
	if err != nil {
		t.Fatalf("first backup failed: %v", err)
	}

	// Create second backup (same second — should get counter suffix or different timestamp).
	second, err := backupSQLite(dbPath)
	if err != nil {
		t.Fatalf("second backup failed: %v", err)
	}

	if first == second {
		t.Errorf("two backups have identical paths: %s", first)
	}

	// Both files should exist.
	for _, p := range []string{first, second} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("backup missing: %s", p)
		}
	}
}

// --- verifyServerTarget tests ---

func TestVerifyServerTarget_NoServerRunning(t *testing.T) {
	// Use a dynamically allocated port guaranteed to be free
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // Close immediately — port is now free but known

	err = verifyServerTarget("beads", port, "root", "")
	if err != nil {
		t.Fatalf("expected nil for no server on free port %d, got: %v", port, err)
	}
}

func TestVerifyServerTarget_PortZero(t *testing.T) {
	err := verifyServerTarget("beads", 0, "root", "")
	if err != nil {
		t.Fatalf("expected nil for port 0, got: %v", err)
	}
}

func TestVerifyServerTarget_EmptyDBName(t *testing.T) {
	err := verifyServerTarget("", 3307, "root", "")
	if err == nil {
		t.Fatal("expected error for empty database name, got nil")
	}
	if !strings.Contains(err.Error(), "empty database name") {
		t.Errorf("expected 'empty database name' error, got: %v", err)
	}
}

func TestVerifyServerTarget_NonMySQLServer(t *testing.T) {
	// Start a TCP listener that accepts connections but doesn't speak MySQL
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	// Accept connections in background (just close them immediately)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// verifyServerTarget should return an error (can't query non-MySQL server)
	err = verifyServerTarget("beads", port, "root", "")
	if err == nil {
		t.Error("expected error when connecting to non-MySQL server, got nil")
	}
}

// --- verifyMigrationData tests ---

func TestVerifyMigrationData_EmptySource(t *testing.T) {
	// Empty source data should pass without connecting to anything
	data := &migrationData{issueCount: 0}
	err := verifyMigrationData(data, "beads", "127.0.0.1", 0, "root", "")
	if err != nil {
		t.Fatalf("expected nil for empty source, got: %v", err)
	}
}

func TestVerifyMigrationData_ConnectionFailure(t *testing.T) {
	// Use a port that nothing is listening on
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	data := &migrationData{
		issueCount: 5,
		issues:     make([]*types.Issue, 5),
	}
	err = verifyMigrationData(data, "beads", "127.0.0.1", port, "root", "")
	if err == nil {
		t.Fatal("expected error when Dolt server is unreachable, got nil")
	}
}

// --- verifyMigrationCounts tests ---

func TestVerifyMigrationCounts_DepsOnlyMismatch(t *testing.T) {
	err := verifyMigrationCounts(10, 5, 10, 3)
	if err == nil {
		t.Fatal("expected error when dolt has fewer deps, got nil")
	}
	if !strings.Contains(err.Error(), "dependency count mismatch") {
		t.Errorf("error should mention dependency count mismatch, got: %v", err)
	}
}

func TestVerifyMigrationCounts_BothMismatch(t *testing.T) {
	err := verifyMigrationCounts(10, 5, 8, 3)
	if err == nil {
		t.Fatal("expected error when both counts mismatch, got nil")
	}
	// Should report both errors
	if !strings.Contains(err.Error(), "issue count") {
		t.Errorf("error should mention issue count, got: %v", err)
	}
	if !strings.Contains(err.Error(), "dependency count") {
		t.Errorf("error should mention dependency count, got: %v", err)
	}
}

func TestVerifyMigrationCounts_Match(t *testing.T) {
	err := verifyMigrationCounts(10, 5, 10, 5)
	if err != nil {
		t.Fatalf("expected no error for matching counts, got: %v", err)
	}
}

func TestVerifyMigrationCounts_DoltHasMore(t *testing.T) {
	err := verifyMigrationCounts(10, 5, 15, 8)
	if err != nil {
		t.Fatalf("expected no error when dolt has more, got: %v", err)
	}
}

func TestVerifyMigrationCounts_DoltHasLess(t *testing.T) {
	err := verifyMigrationCounts(10, 5, 8, 5)
	if err == nil {
		t.Fatal("expected error when dolt has fewer issues, got nil")
	}
	if !strings.Contains(err.Error(), "issue count mismatch") {
		t.Errorf("error should mention issue count mismatch, got: %v", err)
	}
}

func TestVerifyMigrationCounts_ZeroSource(t *testing.T) {
	err := verifyMigrationCounts(0, 0, 0, 0)
	if err != nil {
		t.Fatalf("expected no error for zero source, got: %v", err)
	}
}

// --- finalizeMigration tests ---

// setupBeadsDir creates a temp directory with a .beads subdirectory containing
// a default metadata.json and an empty config.yaml. It also initializes
// the config package's viper instance via BEADS_DIR env var.
func setupBeadsDir(t *testing.T) (tmpDir, beadsDir, sqlitePath string) {
	t.Helper()

	tmpDir = t.TempDir()
	beadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create metadata.json with sqlite backend.
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendSQLite
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	// Create config.yaml (empty, required by SaveConfigValue).
	configYaml := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configYaml, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	// Create fake sqlite file.
	sqlitePath = filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(sqlitePath, []byte("fake-sqlite"), 0600); err != nil {
		t.Fatal(err)
	}

	// Initialize config package pointing at our temp beads dir.
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}

	return tmpDir, beadsDir, sqlitePath
}

func TestFinalizeMigration_UpdatesMetadata(t *testing.T) {
	_, beadsDir, sqlitePath := setupBeadsDir(t)

	if err := finalizeMigration(beadsDir, sqlitePath, "testdb"); err != nil {
		t.Fatalf("finalizeMigration failed: %v", err)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("loading metadata after migration: %v", err)
	}

	if cfg.Backend != configfile.BackendDolt {
		t.Errorf("backend = %q, want %q", cfg.Backend, configfile.BackendDolt)
	}
	if cfg.Database != "dolt" {
		t.Errorf("database = %q, want %q", cfg.Database, "dolt")
	}
	if cfg.DoltDatabase != "testdb" {
		t.Errorf("dolt_database = %q, want %q", cfg.DoltDatabase, "testdb")
	}
}

func TestFinalizeMigration_RenamesSQLite(t *testing.T) {
	_, beadsDir, sqlitePath := setupBeadsDir(t)

	if err := finalizeMigration(beadsDir, sqlitePath, "testdb"); err != nil {
		t.Fatalf("finalizeMigration failed: %v", err)
	}

	// Original should be gone.
	if _, err := os.Stat(sqlitePath); !os.IsNotExist(err) {
		t.Error("sqlite file should have been renamed")
	}

	// .migrated should exist.
	migratedPath := sqlitePath + ".migrated"
	if _, err := os.Stat(migratedPath); err != nil {
		t.Errorf("migrated file missing: %v", err)
	}
}

func TestFinalizeMigration_WritesConfigYaml(t *testing.T) {
	_, beadsDir, sqlitePath := setupBeadsDir(t)

	if err := finalizeMigration(beadsDir, sqlitePath, "testdb"); err != nil {
		t.Fatalf("finalizeMigration failed: %v", err)
	}

	configYaml := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configYaml)
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}

	if !strings.Contains(string(data), "sync") {
		t.Errorf("config.yaml should contain sync.mode key, got: %s", data)
	}
	if !strings.Contains(string(data), "dolt-native") {
		t.Errorf("config.yaml should contain dolt-native value for sync.mode, got: %s", data)
	}
}

func TestFinalizeMigration_WritesDoltNativeSyncMode(t *testing.T) {
	_, beadsDir, sqlitePath := setupBeadsDir(t)

	if err := finalizeMigration(beadsDir, sqlitePath, "testdb"); err != nil {
		t.Fatalf("finalizeMigration failed: %v", err)
	}

	configYaml := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configYaml)
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}

	// Must write "dolt-native", NOT "dolt" (which is not a valid sync.mode)
	if !strings.Contains(string(data), "dolt-native") {
		t.Errorf("config.yaml should contain sync.mode=dolt-native, got: %s", data)
	}
}

func TestFinalizeMigration_PreservesServerConfig(t *testing.T) {
	_, beadsDir, sqlitePath := setupBeadsDir(t)

	// Set server config fields before finalization
	cfg, _ := configfile.Load(beadsDir)
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "192.168.1.50"
	cfg.DoltServerPort = 13306
	cfg.DoltServerUser = "beads_admin"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	if err := finalizeMigration(beadsDir, sqlitePath, "testdb"); err != nil {
		t.Fatalf("finalizeMigration failed: %v", err)
	}

	loaded, loadErr := configfile.Load(beadsDir)
	if loadErr != nil {
		t.Fatalf("failed to load config: %v", loadErr)
	}
	if loaded.DoltMode != configfile.DoltModeServer {
		t.Errorf("DoltMode should be preserved, got %q", loaded.DoltMode)
	}
	if loaded.DoltServerHost != "192.168.1.50" {
		t.Errorf("DoltServerHost should be preserved, got %q", loaded.DoltServerHost)
	}
	if loaded.DoltServerPort != 13306 {
		t.Errorf("DoltServerPort should be preserved, got %d", loaded.DoltServerPort)
	}
	if loaded.DoltServerUser != "beads_admin" {
		t.Errorf("DoltServerUser should be preserved, got %q", loaded.DoltServerUser)
	}
}

func TestFinalizeMigration_NonexistentSQLite(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	// config.yaml needed by SaveConfigValue
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}

	// SQLite file doesn't exist — rename should fail
	err := finalizeMigration(beadsDir, filepath.Join(beadsDir, "nonexistent.db"), "testdb")
	if err == nil {
		t.Error("expected error when SQLite file doesn't exist for rename")
	}

	// But metadata should still be updated (rename is the last step)
	loaded, loadErr := configfile.Load(beadsDir)
	if loadErr != nil {
		t.Fatalf("failed to load config after partial finalize: %v", loadErr)
	}
	if loaded.Backend != configfile.BackendDolt {
		t.Errorf("metadata should be updated even when rename fails, got backend=%q", loaded.Backend)
	}
}

func TestFinalizeMigration_NoMetadata(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No metadata.json — should use default config
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(sqlitePath, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}

	err := finalizeMigration(beadsDir, sqlitePath, "testdb")
	if err != nil {
		t.Fatalf("finalizeMigration with no existing metadata should work: %v", err)
	}

	loaded, loadErr := configfile.Load(beadsDir)
	if loadErr != nil {
		t.Fatalf("failed to load config: %v", loadErr)
	}
	if loaded.Backend != configfile.BackendDolt {
		t.Errorf("backend should be 'dolt', got %q", loaded.Backend)
	}
}

// --- rollbackMetadata tests ---

func TestRollbackMetadata_NilConfig(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := rollbackMetadata(beadsDir, nil)
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
	if !strings.Contains(err.Error(), "no original config") {
		t.Errorf("expected 'no original config' error, got: %v", err)
	}
}

func TestRollbackMetadata_RestoresOriginal(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Save original config.
	original := configfile.DefaultConfig()
	original.Backend = configfile.BackendSQLite
	original.Database = "beads.db"
	if err := original.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	// Modify metadata (simulating partial migration).
	modified := configfile.DefaultConfig()
	modified.Backend = configfile.BackendDolt
	modified.Database = "dolt"
	if err := modified.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	// Verify it was modified.
	check, _ := configfile.Load(beadsDir)
	if check.Backend != configfile.BackendDolt {
		t.Fatal("setup: metadata should be modified")
	}

	// Rollback.
	if err := rollbackMetadata(beadsDir, original); err != nil {
		t.Fatalf("rollbackMetadata failed: %v", err)
	}

	// Verify restored.
	restored, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("loading after rollback: %v", err)
	}
	if restored.Backend != configfile.BackendSQLite {
		t.Errorf("backend = %q, want %q", restored.Backend, configfile.BackendSQLite)
	}
	if restored.Database != "beads.db" {
		t.Errorf("database = %q, want %q", restored.Database, "beads.db")
	}
}
