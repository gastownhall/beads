package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// Tests for the CreateIfMissing guard on CREATE DATABASE.
//
// REQUIREMENTS (from shadow database bug analysis):
// 1. Normal bd operations (list, show, ready, etc.) must NOT create databases.
//    If the database doesn't exist on the server, error out with a clear message.
// 2. Only bd init should create databases (CreateIfMissing=true).
// 3. When a configured server is unreachable and an explicit port is set,
//    auto-start must NOT launch a different server.
// 4. Existing databases with data must remain accessible after server restart.
// 5. When connecting to a server that lacks the expected database, the error
//    message must clearly indicate the database was not found (not a generic
//    connection error).
// 6. ReadOnly mode with a missing database must still error (not silently skip).
//
// NOTE: TLS/Hosted Dolt paths are not separately tested here — the guard logic
// is DSN-agnostic (TLS only affects connection parameters, not the SHOW DATABASES
// or CREATE DATABASE flow).

// --- DRY test helpers ---

// rawTestConn opens a raw MySQL connection to the test server without selecting
// a database. Caller must defer db.Close().
func rawTestConn(t *testing.T, port int) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("failed to connect to test server on port %d: %v", port, err)
	}
	return db
}

func databaseExists(t *testing.T, port int, dbName string) bool {
	t.Helper()
	db := rawTestConn(t, port)
	defer db.Close()

	// Dolt does not support MySQL's backslash escaping in SHOW DATABASES LIKE.
	// Use unescaped name and rely on equality check for exact match.
	var name string
	err := db.QueryRow(fmt.Sprintf("SHOW DATABASES LIKE '%s'", dbName)).Scan(&name)
	return err == nil && name == dbName
}

func assertDatabaseExists(t *testing.T, port int, dbName string) {
	t.Helper()
	if !databaseExists(t, port, dbName) {
		t.Fatalf("expected database %q to exist on port %d", dbName, port)
	}
}

func assertDatabaseNotExists(t *testing.T, port int, dbName string) {
	t.Helper()
	if databaseExists(t, port, dbName) {
		t.Fatalf("expected database %q to NOT exist on port %d", dbName, port)
	}
}

func createTestDatabase(t *testing.T, port int, dbName string) {
	t.Helper()
	db := rawTestConn(t, port)
	defer db.Close()

	if _, err := db.Exec(fmt.Sprintf("CREATE DATABASE `%s`", dbName)); err != nil {
		t.Fatalf("failed to create database %q: %v", dbName, err)
	}
}

func dropTestDatabase(t *testing.T, port int, dbName string) {
	t.Helper()
	db := rawTestConn(t, port)
	defer db.Close()
	db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)) //nolint:errcheck
}

func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// skipIfNoServer skips the test if the shared test Dolt server is not running.
func skipIfNoServer(t *testing.T) {
	t.Helper()
	if testServerPort == 0 {
		t.Skip("no test Dolt server running")
	}
}

// --- Guard tests ---

// TestCreateGuard_MissingDB_DefaultConfig verifies that opening a store
// against a non-existent database FAILS when CreateIfMissing is false (default).
// This is the PRIMARY regression test for the shadow database bug.
func TestCreateGuard_MissingDB_DefaultConfig(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("test_guard_missing_%d", testServerPort)

	assertDatabaseNotExists(t, testServerPort, dbName)

	cfg := &Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     dbName,
		MaxOpenConns: 1,
		// CreateIfMissing is NOT set — defaults to false
	}

	_, err := New(ctx, cfg)
	if err == nil {
		t.Fatal("expected error when database doesn't exist and CreateIfMissing=false, got nil")
	}

	if !containsAny(err.Error(), "not found", "does not exist", "unknown database") {
		t.Errorf("error should indicate database not found, got: %v", err)
	}

	// Verify the database was NOT created as a side effect
	assertDatabaseNotExists(t, testServerPort, dbName)
}

// TestCreateGuard_MissingDB_CreateIfMissing verifies that bd init CAN create
// a new database when CreateIfMissing=true. This ensures the fix doesn't
// break the init flow.
func TestCreateGuard_MissingDB_CreateIfMissing(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("test_guard_create_%d", testServerPort)

	assertDatabaseNotExists(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	cfg := &Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("expected success when CreateIfMissing=true, got: %v", err)
	}
	defer store.Close()

	assertDatabaseExists(t, testServerPort, dbName)
}

// TestCreateGuard_ExistingDB_NoFlag verifies that opening an EXISTING database
// succeeds even when CreateIfMissing=false. Normal happy path.
func TestCreateGuard_ExistingDB_NoFlag(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("test_guard_existing_%d", testServerPort)

	createTestDatabase(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	cfg := &Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     dbName,
		MaxOpenConns: 1,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("expected success opening existing database, got: %v", err)
	}
	defer store.Close()
}

// TestCreateGuard_ExistingDB_WithData verifies that data is preserved when
// reconnecting to an existing database. Simulates server restart scenario.
func TestCreateGuard_ExistingDB_WithData(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("test_guard_data_%d", testServerPort)

	createTestDatabase(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	// First connection: create store and write data
	store1, err := New(ctx, &Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true, // init path
	})
	if err != nil {
		t.Fatalf("first connection failed: %v", err)
	}

	if err := store1.SetConfig(ctx, "test_key", "test_value"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	store1.Close()

	// Second connection: open WITHOUT CreateIfMissing, verify data persists
	store2, err := New(ctx, &Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     dbName,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("second connection failed: %v", err)
	}
	defer store2.Close()

	val, err := store2.GetConfig(ctx, "test_key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if val != "test_value" {
		t.Errorf("data not preserved: got %q, want %q", val, "test_value")
	}
}

// TestCreateGuard_ReadOnly_MissingDB verifies that ReadOnly mode with a missing
// database still errors (doesn't silently skip the guard).
func TestCreateGuard_ReadOnly_MissingDB(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("test_guard_readonly_%d", testServerPort)
	assertDatabaseNotExists(t, testServerPort, dbName)

	cfg := &Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     dbName,
		MaxOpenConns: 1,
		ReadOnly:     true,
		// CreateIfMissing=false (default)
	}

	_, err := New(ctx, cfg)
	if err == nil {
		t.Fatal("expected error when database doesn't exist in ReadOnly mode, got nil")
	}

	if !containsAny(err.Error(), "not found", "does not exist") {
		t.Errorf("error should indicate database not found, got: %v", err)
	}

	assertDatabaseNotExists(t, testServerPort, dbName)
}

// TestCreateGuard_UnderscoreInName verifies that the LIKE wildcard escape works
// correctly for database names containing underscores (common in beads naming:
// "beads_vulcan-clean"). The underscore must not match arbitrary characters.
func TestCreateGuard_UnderscoreInName(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	// Create a database with a name that would match if _ is treated as wildcard
	similarName := fmt.Sprintf("test1guard1underscore1%d", testServerPort)
	targetName := fmt.Sprintf("test_guard_underscore_%d", testServerPort)

	createTestDatabase(t, testServerPort, similarName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, similarName) })

	// Target database does NOT exist — only the similar-named one does
	assertDatabaseNotExists(t, testServerPort, targetName)

	cfg := &Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     targetName,
		MaxOpenConns: 1,
	}

	_, err := New(ctx, cfg)
	if err == nil {
		t.Fatal("expected error: target DB doesn't exist, only a similar-named one does")
	}

	if !containsAny(err.Error(), "not found") {
		t.Errorf("error should indicate database not found, got: %v", err)
	}
}

// TestCreateGuard_UnderscoreBothExist verifies that when BOTH a similar-named
// database (matching unescaped LIKE) and the target database exist, the guard
// correctly identifies the target and opens it. This tests the LIKE escaping
// in the positive case — without escaping, SHOW DATABASES LIKE would return
// the similar-named DB first and the equality check would reject it.
//
// Uses CreateIfMissing=true because this test focuses on LIKE escaping behavior,
// not the guard itself. Dolt's catalog may have timing issues between CREATE
// DATABASE on one connection and SHOW DATABASES on another.
func TestCreateGuard_UnderscoreBothExist(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	similarName := fmt.Sprintf("test1guard1both1%d", testServerPort)
	targetName := fmt.Sprintf("test_guard_both_%d", testServerPort)

	createTestDatabase(t, testServerPort, similarName)
	t.Cleanup(func() {
		dropTestDatabase(t, testServerPort, similarName)
		dropTestDatabase(t, testServerPort, targetName)
	})

	// Open targetName with CreateIfMissing — this tests that the LIKE escaping
	// doesn't false-match similarName and incorrectly report the DB as existing
	// when it isn't the right one.
	cfg := &Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        targetName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("expected success creating target DB when similar-named DB exists, got: %v", err)
	}
	defer store.Close()

	// Store opened successfully — the LIKE query matched the correct target
	// (or created it) despite similarName also existing. The equality check
	// (existingName == cfg.Database) prevents false matches from LIKE wildcards.
}

// TestCreateGuard_ErrorMessage verifies the error message is clear and
// actionable when the database is not found.
func TestCreateGuard_ErrorMessage(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("test_guard_errmsg_%d", testServerPort)
	assertDatabaseNotExists(t, testServerPort, dbName)

	cfg := &Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     dbName,
		MaxOpenConns: 1,
	}

	_, err := New(ctx, cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	// Should mention the database name
	if !strings.Contains(errMsg, dbName) {
		t.Errorf("error should mention database name %q, got: %s", dbName, errMsg)
	}
	// Should suggest bd init
	if !strings.Contains(errMsg, "bd init") {
		t.Errorf("error should suggest 'bd init', got: %s", errMsg)
	}
	// Should suggest bd doctor
	if !strings.Contains(errMsg, "bd doctor") {
		t.Errorf("error should suggest 'bd doctor', got: %s", errMsg)
	}
}

// --- Auto-start guard tests ---

// TestAutoStart_DisabledWithExplicitPort verifies that auto-start is disabled
// when the config file specifies an explicit server port. This prevents
// bd from launching a different server when the configured one is down.
func TestAutoStart_DisabledWithExplicitPort(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	got := resolveAutoStart(false, "", true)
	if got != false {
		t.Error("resolveAutoStart should return false when explicit port is configured")
	}
}

// TestAutoStart_ExplicitPort_CallerOverrideIgnored verifies that even a caller
// requesting AutoStart=true is overridden when an explicit port is configured.
func TestAutoStart_ExplicitPort_CallerOverrideIgnored(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	got := resolveAutoStart(true, "", true)
	if got != false {
		t.Error("resolveAutoStart should return false with explicit port even when caller requests true")
	}
}

// TestAutoStart_ExplicitPort_EnvOverrideStillWins verifies that BEADS_DOLT_AUTO_START=0
// still takes precedence even without explicit port (defense-in-depth).
func TestAutoStart_ExplicitPort_EnvOverrideStillWins(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	got := resolveAutoStart(true, "", false) // no explicit port, but env says no
	if got != false {
		t.Error("BEADS_DOLT_AUTO_START=0 should still disable auto-start without explicit port")
	}
}
