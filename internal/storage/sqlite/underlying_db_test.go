package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestUnderlyingDB_BasicAccess tests that UnderlyingDB returns a usable connection
func TestUnderlyingDB_BasicAccess(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-underlying-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	// Get underlying DB
	db := store.UnderlyingDB()
	if db == nil {
		t.Fatal("UnderlyingDB() returned nil")
	}

	// Verify we can query it
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query via UnderlyingDB: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
}

// TestUnderlyingDB_CreateExtensionTable tests creating a VC-style extension table
func TestUnderlyingDB_CreateExtensionTable(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-extension-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create a test issue first
	issue := &types.Issue{
		Title:       "Test issue",
		Description: "For extension testing",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Get underlying DB and create extension table
	db := store.UnderlyingDB()

	schema := `
		CREATE TABLE IF NOT EXISTS vc_executions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			status TEXT NOT NULL,
			agent_id TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_vc_executions_issue ON vc_executions(issue_id);
	`

	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("Failed to create extension table: %v", err)
	}

	// Insert a row linking to our issue
	result, err := db.Exec(`
		INSERT INTO vc_executions (issue_id, status, agent_id)
		VALUES (?, ?, ?)
	`, issue.ID, "pending", "test-agent")
	if err != nil {
		t.Fatalf("Failed to insert into extension table: %v", err)
	}

	id, _ := result.LastInsertId()
	if id == 0 {
		t.Error("Expected non-zero insert ID")
	}

	// Verify FK enforcement - try to insert with invalid issue_id
	_, err = db.Exec(`
		INSERT INTO vc_executions (issue_id, status, agent_id)
		VALUES (?, ?, ?)
	`, "invalid-id", "pending", "test-agent")
	if err == nil {
		t.Error("Expected FK constraint violation, got nil error")
	}

	// Query across layers (join)
	var title string
	var status string
	err = db.QueryRow(`
		SELECT i.title, e.status
		FROM issues i
		JOIN vc_executions e ON i.id = e.issue_id
		WHERE i.id = ?
	`, issue.ID).Scan(&title, &status)
	if err != nil {
		t.Fatalf("Failed to join across layers: %v", err)
	}

	if title != issue.Title {
		t.Errorf("Expected title %q, got %q", issue.Title, title)
	}
	if status != "pending" {
		t.Errorf("Expected status 'pending', got %q", status)
	}
}

// TestUnderlyingDB_ConcurrentAccess tests concurrent access to UnderlyingDB
func TestUnderlyingDB_ConcurrentAccess(t *testing.T) {
	// Skip on Windows - SQLite locking is more aggressive there
	// Production works fine (WAL mode + busy_timeout), but this test
	// is too aggressive for Windows CI environment
	if os.Getenv("GOOS") == "windows" || filepath.Separator == '\\' {
		t.Skip("Skipping concurrent test on Windows due to SQLite locking")
	}

	tmpDir, err := os.MkdirTemp("", "beads-concurrent-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()
	db := store.UnderlyingDB()

	// Create some test issues
	for i := 0; i < 10; i++ {
		issue := &types.Issue{
			Title:     "Test issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Spawn concurrent goroutines using both storage and raw DB
	var wg sync.WaitGroup
	errors := make(chan error, 50)

	// 10 goroutines querying via UnderlyingDB
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
				errors <- err
			}
		}()
	}

	// 10 goroutines using storage methods
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.SearchIssues(ctx, "", types.IssueFilter{}); err != nil {
				errors <- err
			}
		}()
	}

	// Wait for completion
	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}
}

// TestUnderlyingDB_AfterClose tests behavior after storage is closed
func TestUnderlyingDB_AfterClose(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-close-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Get DB reference before closing
	db := store.UnderlyingDB()

	// Close storage
	if err := store.Close(); err != nil {
		t.Fatalf("Failed to close storage: %v", err)
	}

	// Try to use DB - should fail
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count)
	if err == nil {
		t.Error("Expected error after close, got nil")
	}
}

// TestUnderlyingDB_LongTxDoesNotDeadlock tests that long read tx doesn't block writes forever
func TestUnderlyingDB_LongTxDoesNotDeadlock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-tx-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()
	db := store.UnderlyingDB()

	// Start a long-running read transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to begin tx: %v", err)
	}
	defer tx.Rollback()

	// Query in the transaction
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
		t.Fatalf("Failed to query in tx: %v", err)
	}

	// Try to create an issue via storage (should not deadlock due to WAL + busy_timeout)
	done := make(chan error, 1)
	go func() {
		issue := &types.Issue{
			Title:     "Test during long tx",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		done <- store.CreateIssue(ctx, issue, "test")
	}()

	// Wait with timeout
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("CreateIssue failed during long tx: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("CreateIssue deadlocked or timed out")
	}
}

// TestUnderlyingConn_BasicAccess tests that UnderlyingConn returns a usable connection
func TestUnderlyingConn_BasicAccess(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-conn-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Get a scoped connection
	conn, err := store.UnderlyingConn(ctx)
	if err != nil {
		t.Fatalf("UnderlyingConn() failed: %v", err)
	}
	defer conn.Close()

	// Verify we can query it
	var count int
	err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query via UnderlyingConn: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
}

// TestUnderlyingConn_DDLOperations tests using UnderlyingConn for DDL
func TestUnderlyingConn_DDLOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-conn-ddl-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create a test issue first for FK reference
	issue := &types.Issue{
		Title:       "Test issue",
		Description: "For extension testing",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Get a scoped connection for DDL
	conn, err := store.UnderlyingConn(ctx)
	if err != nil {
		t.Fatalf("UnderlyingConn() failed: %v", err)
	}
	defer conn.Close()

	// Create extension table using the scoped connection
	schema := `
		CREATE TABLE IF NOT EXISTS vc_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			version TEXT NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_vc_migrations_issue ON vc_migrations(issue_id);
	`

	if _, err := conn.ExecContext(ctx, schema); err != nil {
		t.Fatalf("Failed to create extension table: %v", err)
	}

	// Insert using the same connection
	result, err := conn.ExecContext(ctx, `
		INSERT INTO vc_migrations (issue_id, version)
		VALUES (?, ?)
	`, issue.ID, "v1.0.0")
	if err != nil {
		t.Fatalf("Failed to insert into extension table: %v", err)
	}

	id, _ := result.LastInsertId()
	if id == 0 {
		t.Error("Expected non-zero insert ID")
	}

	// Verify the data persists after connection close
	conn.Close()

	// Use UnderlyingDB to verify
	db := store.UnderlyingDB()
	var version string
	err = db.QueryRowContext(ctx, `
		SELECT version FROM vc_migrations WHERE issue_id = ?
	`, issue.ID).Scan(&version)
	if err != nil {
		t.Fatalf("Failed to query after connection close: %v", err)
	}

	if version != "v1.0.0" {
		t.Errorf("Expected version 'v1.0.0', got %q", version)
	}
}

// TestUnderlyingConn_ContextCancellation tests that context cancellation works
func TestUnderlyingConn_ContextCancellation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-conn-ctx-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	// Create a context that's already canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Try to get connection with canceled context
	conn, err := store.UnderlyingConn(ctx)
	if err == nil {
		conn.Close()
		t.Error("Expected error with canceled context, got nil")
	}
}

// TestUnderlyingConn_MultipleConnections tests multiple connections don't interfere
func TestUnderlyingConn_MultipleConnections(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-multi-conn-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Get multiple connections
	conn1, err := store.UnderlyingConn(ctx)
	if err != nil {
		t.Fatalf("Failed to get conn1: %v", err)
	}
	defer conn1.Close()

	conn2, err := store.UnderlyingConn(ctx)
	if err != nil {
		t.Fatalf("Failed to get conn2: %v", err)
	}
	defer conn2.Close()

	// Both should be able to query independently
	var count1, count2 int
	if err := conn1.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count1); err != nil {
		t.Errorf("conn1 query failed: %v", err)
	}
	if err := conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count2); err != nil {
		t.Errorf("conn2 query failed: %v", err)
	}

	if count1 != count2 {
		t.Errorf("Connections see different data: %d vs %d", count1, count2)
	}
}
