package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	_ "modernc.org/sqlite"
)

// TestMigrateIssueCountersTable tests that the migration properly creates
// the issue_counters table and syncs it from existing issues
func TestMigrateIssueCountersTable(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "beads-migration-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	// Step 1: Create database with old schema (no issue_counters table)
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Create minimal schema (issues table only, no issue_counters)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			design TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER NOT NULL DEFAULT 2,
			issue_type TEXT NOT NULL DEFAULT 'task',
			assignee TEXT,
			estimated_minutes INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			closed_at DATETIME
		);

		CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("failed to create old schema: %v", err)
	}

	// Insert some existing issues with IDs
	_, err = db.Exec(`
		INSERT INTO issues (id, title, status, priority, issue_type)
		VALUES
			('bd-5', 'Issue 5', 'open', 2, 'task'),
			('bd-10', 'Issue 10', 'open', 2, 'task'),
			('bd-15', 'Issue 15', 'open', 2, 'task'),
			('custom-3', 'Custom 3', 'open', 2, 'task'),
			('custom-7', 'Custom 7', 'open', 2, 'task')
	`)
	if err != nil {
		t.Fatalf("failed to insert test issues: %v", err)
	}

	// Verify issue_counters table doesn't exist yet
	var tableName string
	err = db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='issue_counters'
	`).Scan(&tableName)
	if err != sql.ErrNoRows {
		t.Fatalf("Expected issue_counters table to not exist, but it does")
	}

	db.Close()

	// Step 2: Open database with New() which should trigger migration
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage (migration failed): %v", err)
	}
	defer store.Close()

	// Step 3: Verify issue_counters table now exists
	err = store.db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='issue_counters'
	`).Scan(&tableName)
	if err != nil {
		t.Fatalf("Expected issue_counters table to exist after migration: %v", err)
	}

	// Step 4: Verify counters were synced correctly
	ctx := context.Background()

	// Check bd prefix counter (max is bd-15)
	var bdCounter int
	err = store.db.QueryRowContext(ctx,
		`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&bdCounter)
	if err != nil {
		t.Fatalf("Failed to query bd counter: %v", err)
	}
	if bdCounter != 15 {
		t.Errorf("Expected bd counter to be 15, got %d", bdCounter)
	}

	// Check custom prefix counter (max is custom-7)
	var customCounter int
	err = store.db.QueryRowContext(ctx,
		`SELECT last_id FROM issue_counters WHERE prefix = 'custom'`).Scan(&customCounter)
	if err != nil {
		t.Fatalf("Failed to query custom counter: %v", err)
	}
	if customCounter != 7 {
		t.Errorf("Expected custom counter to be 7, got %d", customCounter)
	}

	// Step 5: Verify next auto-generated IDs are correct
	// Set prefix to bd
	err = store.SetConfig(ctx, "issue_prefix", "bd")
	if err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	issue := &types.Issue{
		Title:     "New issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err = store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Should be bd-16 (after bd-15)
	if issue.ID != "bd-16" {
		t.Errorf("Expected bd-16, got %s", issue.ID)
	}
}

// TestMigrateIssueCountersTableEmptyDB tests migration on a fresh database
func TestMigrateIssueCountersTableEmptyDB(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-migration-empty-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	// Create a fresh database with New() - should create table with no issues
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	// Verify table exists
	var tableName string
	err = store.db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='issue_counters'
	`).Scan(&tableName)
	if err != nil {
		t.Fatalf("Expected issue_counters table to exist: %v", err)
	}

	// Verify no counters exist (since no issues)
	var count int
	err = store.db.QueryRow(`SELECT COUNT(*) FROM issue_counters`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query counters: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 counters in empty DB, got %d", count)
	}

	// Create first issue - should work fine
	ctx := context.Background()
	issue := &types.Issue{
		Title:     "First issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	err = store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Should be bd-1
	if issue.ID != "bd-1" {
		t.Errorf("Expected bd-1, got %s", issue.ID)
	}
}

// TestMigrateIssueCountersTableIdempotent verifies that running migration
// multiple times is safe and doesn't corrupt data
func TestMigrateIssueCountersTableIdempotent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-migration-idempotent-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	// Create database and migrate
	store1, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	// Create some issues
	ctx := context.Background()
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store1.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	firstID := issue.ID // Should be bd-1
	store1.Close()

	// Re-open database (triggers migration again)
	store2, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to re-open storage: %v", err)
	}
	defer store2.Close()

	// Verify counter is still correct
	var bdCounter int
	err = store2.db.QueryRowContext(ctx,
		`SELECT last_id FROM issue_counters WHERE prefix = 'bd'`).Scan(&bdCounter)
	if err != nil {
		t.Fatalf("Failed to query bd counter: %v", err)
	}
	if bdCounter != 1 {
		t.Errorf("Expected bd counter to be 1 after idempotent migration, got %d", bdCounter)
	}

	// Create another issue
	issue2 := &types.Issue{
		Title:     "Second issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store2.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Should be bd-2 (not bd-1 again)
	if issue2.ID != "bd-2" {
		t.Errorf("Expected bd-2, got %s", issue2.ID)
	}

	// Verify first issue still exists
	firstIssue, err := store2.GetIssue(ctx, firstID)
	if err != nil {
		t.Fatalf("Failed to get first issue: %v", err)
	}
	if firstIssue == nil {
		t.Errorf("First issue was lost after re-opening database")
	}
}
