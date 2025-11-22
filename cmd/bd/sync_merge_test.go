package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// setupTestStore creates a test storage with issue_prefix configured
func setupTestStore(t *testing.T, dbPath string) *sqlite.SQLiteStorage {
	t.Helper()
	
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	
	return store
}

// TestDBNeedsExport_InSync verifies dbNeedsExport returns false when DB and JSONL are in sync
func TestDBNeedsExport_InSync(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create an issue in DB
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Wait a moment to ensure DB mtime isn't newer
	time.Sleep(10 * time.Millisecond)

	// Touch JSONL to make it newer than DB
	now := time.Now()
	if err := os.Chtimes(jsonlPath, now, now); err != nil {
		t.Fatalf("Failed to touch JSONL: %v", err)
	}

	// DB and JSONL should be in sync
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if needsExport {
		t.Errorf("Expected needsExport=false (DB and JSONL in sync), got true")
	}
}

// TestDBNeedsExport_DBNewer verifies dbNeedsExport returns true when DB is modified
func TestDBNeedsExport_DBNewer(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create and export issue
	issue1 := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue1, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Wait and modify DB
	time.Sleep(10 * time.Millisecond)
	issue2 := &types.Issue{
		Title:     "Another Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}

	// DB is newer, should need export
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if !needsExport {
		t.Errorf("Expected needsExport=true (DB modified), got false")
	}
}

// TestDBNeedsExport_CountMismatch verifies dbNeedsExport returns true when counts differ
func TestDBNeedsExport_CountMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create and export issue
	issue1 := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue1, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Add another issue to DB but don't export
	issue2 := &types.Issue{
		Title:     "Another Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}

	// Make JSONL appear newer (but counts differ)
	time.Sleep(10 * time.Millisecond)
	now := time.Now().Add(1 * time.Hour) // Way in the future
	if err := os.Chtimes(jsonlPath, now, now); err != nil {
		t.Fatalf("Failed to touch JSONL: %v", err)
	}

	// Counts mismatch, should need export
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if !needsExport {
		t.Errorf("Expected needsExport=true (count mismatch), got false")
	}
}

// TestDBNeedsExport_NoJSONL verifies dbNeedsExport returns true when JSONL doesn't exist
func TestDBNeedsExport_NoJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create issue but don't export
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// JSONL doesn't exist, should need export
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if !needsExport {
		t.Fatalf("Expected needsExport=true (JSONL missing), got false")
	}
}
