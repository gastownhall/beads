package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// newTestDoltDB creates a temporary database on the test Dolt server.
// Returns a *sql.DB connection to the database and a cleanup function.
// Skips the test if the test server isn't running.
func newTestDoltDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	if testServerPort == 0 {
		t.Skip("Test Dolt server not running, skipping test")
	}
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	dbName := uniqueTestDBName(t)

	adminDSN := fmt.Sprintf("root@tcp(127.0.0.1:%d)/", testServerPort)
	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("failed to connect to test Dolt server: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
		admin.Close()
		t.Fatalf("failed to create test database %s: %v", dbName, err)
	}
	admin.Close()

	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s", testServerPort, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("failed to connect to test database %s: %v", dbName, err)
	}

	return db, func() {
		db.Close()
		// Skip DROP DATABASE — rapid CREATE/DROP cycles crash the Dolt container.
		// Orphan databases are cleaned up when the container terminates.
	}
}

// TestExecContext_Commit verifies that execContext wraps writes in an explicit
// BEGIN/COMMIT, making them durable even when the session's autocommit is off.
func TestExecContext_Commit(t *testing.T) {
	db, cleanup := newTestDoltDB(t)
	defer cleanup()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE items (id VARCHAR(255) PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	store := &DoltStore{db: db}

	result, err := store.execContext(ctx, "INSERT INTO items (id, val) VALUES (?, ?)", "x1", "hello")
	if err != nil {
		t.Fatalf("execContext failed: %v", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row affected, got %d", n)
	}

	// Row must be visible in a separate query (i.e. the transaction was committed).
	var val string
	if err := db.QueryRowContext(ctx, "SELECT val FROM items WHERE id = ?", "x1").Scan(&val); err != nil {
		t.Fatalf("row not visible after execContext commit: %v", err)
	}
	if val != "hello" {
		t.Errorf("expected 'hello', got %q", val)
	}
}

// TestExecContext_RollbackOnError verifies that when the statement fails,
// execContext rolls back the transaction and returns the error.
func TestExecContext_RollbackOnError(t *testing.T) {
	db, cleanup := newTestDoltDB(t)
	defer cleanup()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE items (id VARCHAR(255) PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	store := &DoltStore{db: db}

	// First insert succeeds.
	if _, err := store.execContext(ctx, "INSERT INTO items (id, val) VALUES (?, ?)", "dupe", "first"); err != nil {
		t.Fatalf("initial insert failed: %v", err)
	}

	// Second insert with the same PK must fail and roll back.
	if _, err := store.execContext(ctx, "INSERT INTO items (id, val) VALUES (?, ?)", "dupe", "second"); err == nil {
		t.Fatal("expected error for duplicate primary key, got nil")
	}

	// The original row must survive.
	var val string
	if err := db.QueryRowContext(ctx, "SELECT val FROM items WHERE id = ?", "dupe").Scan(&val); err != nil {
		t.Fatalf("original row missing after rollback: %v", err)
	}
	if val != "first" {
		t.Errorf("expected 'first' to survive rollback, got %q", val)
	}
}

// TestGetAdaptiveIDLength exercises every branch in getAdaptiveIDLengthFromTable.
func TestGetAdaptiveIDLength(t *testing.T) {
	db, cleanup := newTestDoltDB(t)
	defer cleanup()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE test_ids (id VARCHAR(255) NOT NULL)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	populate := func(n int) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "DELETE FROM test_ids"); err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		// Bulk-insert in a single transaction so large counts stay fast.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		for i := 0; i < n; i++ {
			if _, err := tx.ExecContext(ctx, "INSERT INTO test_ids VALUES (?)", fmt.Sprintf("pfx-%06d", i)); err != nil {
				_ = tx.Rollback()
				t.Fatalf("INSERT %d: %v", i, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	tests := []struct {
		count   int
		wantLen int
	}{
		{0, 4},
		{50, 4},
		{99, 4},
		{100, 5},
		{500, 5},
		{999, 5},
		{1000, 6},
		{5000, 6},
		{9999, 6},
		{10000, 7},
	}

	for _, tt := range tests {
		populate(tt.count)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("count=%d: BeginTx: %v", tt.count, err)
		}
		got := getAdaptiveIDLengthFromTable(ctx, tx, "test_ids", "pfx-")
		_ = tx.Rollback()
		if got != tt.wantLen {
			t.Errorf("count=%d: want length %d, got %d", tt.count, tt.wantLen, got)
		}
	}
}

// TestGetAdaptiveIDLength_QueryError verifies the fallback when the query fails
// (e.g. the table does not exist).
func TestGetAdaptiveIDLength_QueryError(t *testing.T) {
	db, cleanup := newTestDoltDB(t)
	defer cleanup()

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Table does not exist — function must return the safe default of 4.
	got := getAdaptiveIDLengthFromTable(ctx, tx, "nonexistent_table", "pfx-")
	if got != 4 {
		t.Errorf("expected fallback length 4, got %d", got)
	}
}

// TestApplyConfigDefaults_TestModeUseSentinelPort verifies that
// applyConfigDefaults uses sentinel port 1 when BEADS_TEST_MODE=1 but
// BEADS_DOLT_PORT is not set, preventing accidental connections to
// the production server while allowing tests to handle connection errors.
func TestApplyConfigDefaults_TestModeUseSentinelPort(t *testing.T) {
	// Save and restore env vars.
	origTestMode := os.Getenv("BEADS_TEST_MODE")
	origPort := os.Getenv("BEADS_DOLT_PORT")
	defer func() {
		os.Setenv("BEADS_TEST_MODE", origTestMode)
		if origPort == "" {
			os.Unsetenv("BEADS_DOLT_PORT")
		} else {
			os.Setenv("BEADS_DOLT_PORT", origPort)
		}
	}()

	os.Setenv("BEADS_TEST_MODE", "1")
	os.Unsetenv("BEADS_DOLT_PORT")

	cfg := &Config{} // ServerPort defaults to 0
	applyConfigDefaults(cfg)

	if cfg.ServerPort != 1 {
		t.Errorf("expected sentinel port 1 in test mode without BEADS_DOLT_PORT, got %d", cfg.ServerPort)
	}
}

// TestApplyConfigDefaults_TestModeWithPort verifies that applyConfigDefaults
// does NOT panic when BEADS_TEST_MODE=1 and BEADS_DOLT_PORT is properly set.
func TestApplyConfigDefaults_TestModeWithPort(t *testing.T) {
	origTestMode := os.Getenv("BEADS_TEST_MODE")
	origPort := os.Getenv("BEADS_DOLT_PORT")
	defer func() {
		os.Setenv("BEADS_TEST_MODE", origTestMode)
		if origPort == "" {
			os.Unsetenv("BEADS_DOLT_PORT")
		} else {
			os.Setenv("BEADS_DOLT_PORT", origPort)
		}
	}()

	os.Setenv("BEADS_TEST_MODE", "1")
	os.Setenv("BEADS_DOLT_PORT", "13307")

	cfg := &Config{}
	applyConfigDefaults(cfg)

	if cfg.ServerPort != 13307 {
		t.Errorf("expected ServerPort=13307, got %d", cfg.ServerPort)
	}
}

// TestApplyConfigDefaults_TestModeBlocksProdPort verifies that BEADS_TEST_MODE=1
// forces port 1 even when BEADS_DOLT_PORT is explicitly set to the production port.
// This is the fix for Clown Show #14: The orchestrator's beads module injects
// BEADS_DOLT_PORT=3307 from metadata.json, bypassing the test mode guard.
func TestApplyConfigDefaults_TestModeBlocksProdPort(t *testing.T) {
	origTestMode := os.Getenv("BEADS_TEST_MODE")
	origPort := os.Getenv("BEADS_DOLT_PORT")
	defer func() {
		if origTestMode == "" {
			os.Unsetenv("BEADS_TEST_MODE")
		} else {
			os.Setenv("BEADS_TEST_MODE", origTestMode)
		}
		if origPort == "" {
			os.Unsetenv("BEADS_DOLT_PORT")
		} else {
			os.Setenv("BEADS_DOLT_PORT", origPort)
		}
	}()

	os.Setenv("BEADS_TEST_MODE", "1")
	os.Setenv("BEADS_DOLT_PORT", "3307") // Production port

	cfg := &Config{}
	applyConfigDefaults(cfg)

	if cfg.ServerPort != 1 {
		t.Errorf("BEADS_TEST_MODE=1 with BEADS_DOLT_PORT=3307 should force port 1, got %d", cfg.ServerPort)
	}
}

// TestApplyConfigDefaults_EnvOverridesConfig verifies that BEADS_DOLT_PORT
// overrides a port already set by metadata.json, even outside test mode.
// This is the fix for hq-27t (test pollution): callers like the orchestrator set
// BEADS_DOLT_PORT to route bd to a test server instead of production.
func TestApplyConfigDefaults_EnvOverridesConfig(t *testing.T) {
	origTestMode := os.Getenv("BEADS_TEST_MODE")
	origPort := os.Getenv("BEADS_DOLT_PORT")
	defer func() {
		if origTestMode == "" {
			os.Unsetenv("BEADS_TEST_MODE")
		} else {
			os.Setenv("BEADS_TEST_MODE", origTestMode)
		}
		if origPort == "" {
			os.Unsetenv("BEADS_DOLT_PORT")
		} else {
			os.Setenv("BEADS_DOLT_PORT", origPort)
		}
	}()

	os.Unsetenv("BEADS_TEST_MODE") // NOT in test mode
	os.Setenv("BEADS_DOLT_PORT", "19999")

	// Simulate metadata.json having set port to production default
	cfg := &Config{ServerPort: DefaultSQLPort}
	applyConfigDefaults(cfg)

	if cfg.ServerPort != 19999 {
		t.Errorf("expected BEADS_DOLT_PORT=19999 to override config port %d, got %d",
			DefaultSQLPort, cfg.ServerPort)
	}
}

// TestApplyConfigDefaults_ProductionFallback verifies that without
// BEADS_TEST_MODE and no env port, ServerPort stays 0 (ephemeral).
// Auto-start (EnsureRunning) will allocate the port at connection time.
func TestApplyConfigDefaults_ProductionFallback(t *testing.T) {
	origTestMode := os.Getenv("BEADS_TEST_MODE")
	origPort := os.Getenv("BEADS_DOLT_PORT")
	defer func() {
		if origTestMode == "" {
			os.Unsetenv("BEADS_TEST_MODE")
		} else {
			os.Setenv("BEADS_TEST_MODE", origTestMode)
		}
		if origPort == "" {
			os.Unsetenv("BEADS_DOLT_PORT")
		} else {
			os.Setenv("BEADS_DOLT_PORT", origPort)
		}
	}()

	os.Unsetenv("BEADS_TEST_MODE")
	os.Unsetenv("BEADS_DOLT_PORT")

	cfg := &Config{}
	applyConfigDefaults(cfg)

	if cfg.ServerPort != 0 {
		t.Errorf("expected ServerPort=0 (ephemeral, resolved by auto-start), got %d", cfg.ServerPort)
	}
}

// TestExecWithLongTimeoutDSNRewrite verifies that execWithLongTimeout's
// ParseDSN/FormatDSN rewrite produces a valid DSN with readTimeout=5m
// given a DSN from buildServerDSN.
func TestExecWithLongTimeoutDSNRewrite(t *testing.T) {
	cfg := &Config{
		ServerUser: "root",
		ServerHost: "127.0.0.1",
		ServerPort: 3307,
		Database:   "testdb",
	}
	applyConfigDefaults(cfg)

	original := buildServerDSN(cfg, cfg.Database)

	// Simulate the same rewrite that execWithLongTimeout performs.
	parsed, err := mysql.ParseDSN(original)
	if err != nil {
		t.Fatalf("failed to parse original DSN: %v", err)
	}
	parsed.ReadTimeout = 5 * time.Minute
	rewritten := parsed.FormatDSN()

	reParsed, err := mysql.ParseDSN(rewritten)
	if err != nil {
		t.Fatalf("failed to parse rewritten DSN: %v", err)
	}
	if reParsed.ReadTimeout != 5*time.Minute {
		t.Errorf("expected readTimeout=5m, got %v", reParsed.ReadTimeout)
	}
}

func TestShouldStopAutoStartedServerOnClose(t *testing.T) {
	origTestMode := os.Getenv("BEADS_TEST_MODE")
	defer func() {
		if origTestMode == "" {
			os.Unsetenv("BEADS_TEST_MODE")
		} else {
			os.Setenv("BEADS_TEST_MODE", origTestMode)
		}
	}()

	t.Run("normal repo local server stays up", func(t *testing.T) {
		os.Unsetenv("BEADS_TEST_MODE")
		if shouldStopAutoStartedServerOnClose(&Config{Database: "op_broker"}) {
			t.Fatal("expected normal repo-local auto-start to persist after Close")
		}
	})

	t.Run("test mode still owns cleanup", func(t *testing.T) {
		os.Setenv("BEADS_TEST_MODE", "1")
		if !shouldStopAutoStartedServerOnClose(&Config{Database: "op_broker"}) {
			t.Fatal("expected BEADS_TEST_MODE to keep auto-start cleanup enabled")
		}
	})

	t.Run("test database names still clean up", func(t *testing.T) {
		os.Unsetenv("BEADS_TEST_MODE")
		if !shouldStopAutoStartedServerOnClose(&Config{Database: "testdb_abcdef"}) {
			t.Fatal("expected test database names to keep auto-start cleanup enabled")
		}
	})
}
