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
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
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
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
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
	tmpDir, err := os.MkdirTemp("", "beads-concurrent-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
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
	store, err := New(dbPath)
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
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
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
