package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadOnlyDoesNotModifyFile verifies that opening a database in read-only mode
// and performing read operations does not modify the database file.
// This is the fix for GH#804.
func TestReadOnlyDoesNotModifyFile(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir, err := os.MkdirTemp("", "beads-readonly-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// Step 1: Create and initialize the database with some data
	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	// Set prefix and create a test issue
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		store.Close()
		t.Fatalf("failed to create issue: %v", err)
	}

	// Close the store to flush all changes
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// Step 2: Get the file's modification time after initial write
	// Wait a moment to ensure mtime granularity
	time.Sleep(100 * time.Millisecond)

	info1, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("failed to stat database: %v", err)
	}
	mtime1 := info1.ModTime()

	// Step 3: Open in read-only mode and perform read operations
	time.Sleep(100 * time.Millisecond) // Ensure time has passed

	roStore, err := NewReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open read-only: %v", err)
	}

	// Perform various read operations using SearchIssues
	issues, err := roStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		roStore.Close()
		t.Fatalf("failed to search issues: %v", err)
	}
	if len(issues) != 1 {
		roStore.Close()
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	// Read the issue by ID
	_, err = roStore.GetIssue(ctx, issues[0].ID)
	if err != nil {
		roStore.Close()
		t.Fatalf("failed to get issue: %v", err)
	}

	// Get config
	_, err = roStore.GetConfig(ctx, "issue_prefix")
	if err != nil {
		roStore.Close()
		t.Fatalf("failed to get config: %v", err)
	}

	// Close the read-only store
	if err := roStore.Close(); err != nil {
		t.Fatalf("failed to close read-only store: %v", err)
	}

	// Step 4: Verify the file was NOT modified
	info2, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("failed to stat database after read-only operations: %v", err)
	}
	mtime2 := info2.ModTime()

	if !mtime1.Equal(mtime2) {
		t.Errorf("database file was modified during read-only operations!\n"+
			"  before: %v\n  after:  %v\n"+
			"This breaks file watchers (GH#804)",
			mtime1, mtime2)
	}
}

// TestReadOnlyRejectsWrites verifies that write operations fail on read-only connections.
func TestReadOnlyRejectsWrites(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir, err := os.MkdirTemp("", "beads-readonly-write-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// Create and initialize the database
	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// Open in read-only mode
	roStore, err := NewReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open read-only: %v", err)
	}
	defer roStore.Close()

	// Attempt to create an issue - should fail
	issue := &types.Issue{
		Title:     "Should fail",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err = roStore.CreateIssue(ctx, issue, "test-user")
	if err == nil {
		t.Error("expected write to fail on read-only database, but it succeeded")
	}
}

// TestReadOnlyFailsOnNonexistentDB verifies that NewReadOnly returns an error
// when the database file doesn't exist.
func TestReadOnlyFailsOnNonexistentDB(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-readonly-noexist-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "nonexistent.db")
	ctx := context.Background()

	_, err = NewReadOnly(ctx, dbPath)
	if err == nil {
		t.Error("expected error when opening nonexistent database in read-only mode")
	}
}

// TestReadOnlyRejectsInMemory verifies that NewReadOnly rejects in-memory databases.
func TestReadOnlyRejectsInMemory(t *testing.T) {
	ctx := context.Background()

	_, err := NewReadOnly(ctx, ":memory:")
	if err == nil {
		t.Error("expected error when opening in-memory database in read-only mode")
	}
}
