//go:build integration
// +build integration

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

// TestExportUpdatesDatabaseMtime verifies that export updates database mtime
// to be >= JSONL mtime, fixing issues #278, #301, #321
func TestExportUpdatesDatabaseMtime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create and populate database
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database with issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Create a test issue
	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Wait a bit to ensure mtime difference
	time.Sleep(1 * time.Second)

	// Export to JSONL (simulates daemon export)
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Update metadata after export (bd-ymj fix)
	mockLogger := newTestLogger()
	updateExportMetadata(ctx, store, jsonlPath, mockLogger, "")

	// Get JSONL mtime
	jsonlInfo, err := os.Stat(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to stat JSONL after export: %v", err)
	}

	// WITHOUT the fix, JSONL would be newer than DB here
	// Simulating the old buggy behavior before calling TouchDatabaseFile
	dbInfoAfterExport, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Failed to stat database after export: %v", err)
	}

	// In old buggy behavior, JSONL mtime > DB mtime
	t.Logf("Before TouchDatabaseFile: DB mtime=%v, JSONL mtime=%v",
		dbInfoAfterExport.ModTime(), jsonlInfo.ModTime())

	// Now apply the fix
	if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
		t.Fatalf("TouchDatabaseFile failed: %v", err)
	}

	// Get final database mtime
	dbInfoAfterTouch, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Failed to stat database after touch: %v", err)
	}

	t.Logf("After TouchDatabaseFile: DB mtime=%v, JSONL mtime=%v",
		dbInfoAfterTouch.ModTime(), jsonlInfo.ModTime())

	// VERIFY: Database mtime should be >= JSONL mtime
	if dbInfoAfterTouch.ModTime().Before(jsonlInfo.ModTime()) {
		t.Errorf("Database mtime should be >= JSONL mtime after export")
		t.Errorf("DB mtime: %v, JSONL mtime: %v",
			dbInfoAfterTouch.ModTime(), jsonlInfo.ModTime())
	}

	// VERIFY: validatePreExport should now pass (not block on next export)
	if err := validatePreExport(ctx, store, jsonlPath); err != nil {
		t.Errorf("validatePreExport should pass after TouchDatabaseFile, but got error: %v", err)
	}
}

// TestDaemonExportScenario simulates the full daemon auto-export workflow
// that was causing issue #278 (daemon shutting down after export)
func TestDaemonExportScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create and populate database
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database with issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Step 1: User creates an issue (e.g., bd close bd-123)
	now := time.Now()
	issue := &types.Issue{
		ID:        "bd-123",
		Title:     "User created issue",
		Status:    types.StatusClosed,
		Priority:  1,
		IssueType: types.TypeTask,
		ClosedAt:  &now,
	}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Database is now newer than JSONL (JSONL doesn't exist yet)
	time.Sleep(1 * time.Second)

	// Step 2: Daemon auto-exports after delay (30s-4min in real scenario)
	// This simulates the daemon's export cycle
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Daemon export failed: %v", err)
	}

	// Daemon updates metadata after export (bd-ymj fix)
	mockLogger := newTestLogger()
	updateExportMetadata(ctx, store, jsonlPath, mockLogger, "")

	// THIS IS THE FIX: daemon now calls TouchDatabaseFile after export
	if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
		t.Fatalf("TouchDatabaseFile failed: %v", err)
	}

	// Step 3: User runs bd sync shortly after
	// WITHOUT the fix, this would fail with "JSONL is newer than database"
	// WITH the fix, this should succeed
	if err := validatePreExport(ctx, store, jsonlPath); err != nil {
		t.Errorf("Daemon export scenario failed: validatePreExport blocked after daemon export")
		t.Errorf("This is the bug from issue #278/#301/#321: %v", err)
	}

	// Verify we can export again (simulates bd sync)
	jsonlPathTemp := jsonlPath + ".sync"
	if err := exportToJSONLWithStore(ctx, store, jsonlPathTemp); err != nil {
		t.Errorf("Second export (bd sync) failed: %v", err)
	}
	os.Remove(jsonlPathTemp)
}

// TestMultipleExportCycles verifies repeated export cycles don't cause issues
func TestMultipleExportCycles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create and populate database
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize database with issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Run multiple export cycles
	for i := 0; i < 5; i++ {
		// Add an issue
		issue := &types.Issue{
			ID:        "test-" + string(rune('a'+i)),
			Title:     "Test Issue " + string(rune('A'+i)),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("Cycle %d: Failed to create issue: %v", i, err)
		}

		time.Sleep(100 * time.Millisecond)

		// Export (with fix)
		if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
			t.Fatalf("Cycle %d: Export failed: %v", i, err)
		}

		// Update metadata after export (bd-ymj fix)
		mockLogger := newTestLogger()
		updateExportMetadata(ctx, store, jsonlPath, mockLogger, "")

		// Apply fix
		if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
			t.Fatalf("Cycle %d: TouchDatabaseFile failed: %v", i, err)
		}

		// Verify validation passes
		if err := validatePreExport(ctx, store, jsonlPath); err != nil {
			t.Errorf("Cycle %d: validatePreExport failed: %v", i, err)
		}
	}
}
