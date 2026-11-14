package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads"
)

// clearServerModeEnv clears the environment switches that put workspace
// discovery and opens into Dolt server mode. With any of them exported,
// discovery routes to .beads/dolt and OpenBestAvailable connects to (or
// auto-starts) a SQL server instead of using the test's embedded fixture.
func clearServerModeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
}

// TestExampleCompiles ensures the example code compiles and basic API works
func TestExampleCompiles(t *testing.T) {
	clearServerModeEnv(t)

	// Create temporary database for testing
	tmpDir, err := os.MkdirTemp("", "beads-example-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Open storage
	ctx := context.Background()
	store, err := beads.OpenBestAvailable(ctx, beadsDir)
	if err != nil {
		t.Fatalf("Failed to open storage: %v", err)
	}
	defer store.Close()

	// A fresh workspace needs an issue prefix before issues can be created
	// (bd init normally seeds this).
	if err := store.SetConfig(ctx, "issue_prefix", "ex"); err != nil {
		t.Fatalf("Failed to seed issue prefix: %v", err)
	}

	// Create an issue (from example code)
	newIssue := &beads.Issue{
		ID:          "",
		Title:       "Test library-created issue",
		Description: "This verifies the library example works",
		Status:      beads.StatusOpen,
		Priority:    2,
		IssueType:   beads.TypeTask,
	}

	if err := store.CreateIssue(ctx, newIssue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	if newIssue.ID == "" {
		t.Error("Issue ID should be auto-generated")
	}

	// Get ready work (from example code)
	ready, err := store.GetReadyWork(ctx, beads.WorkFilter{
		Status: beads.StatusOpen,
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("Failed to get ready work: %v", err)
	}

	if len(ready) == 0 {
		t.Error("Expected at least one ready issue")
	}

	// Add label (from example code)
	if err := store.AddLabel(ctx, newIssue.ID, "test-label", "test"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}

	labels, err := store.GetLabels(ctx, newIssue.ID)
	if err != nil {
		t.Fatalf("Failed to get labels: %v", err)
	}

	if len(labels) != 1 || labels[0] != "test-label" {
		t.Errorf("Expected label 'test-label', got %v", labels)
	}

	// Update status (from example code)
	updates := map[string]interface{}{
		"status": beads.StatusInProgress,
	}
	if err := store.UpdateIssue(ctx, newIssue.ID, updates, "test"); err != nil {
		t.Fatalf("Failed to update issue: %v", err)
	}

	// Get statistics (from example code)
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("Failed to get statistics: %v", err)
	}

	if stats.TotalIssues == 0 {
		t.Error("Expected at least one total issue")
	}

	// Close issue (from example code)
	if err := store.CloseIssue(ctx, newIssue.ID, "Test complete", "test", ""); err != nil {
		t.Fatalf("Failed to close issue: %v", err)
	}

	// Verify closed
	closed, err := store.GetIssue(ctx, newIssue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if closed.Status != beads.StatusClosed {
		t.Errorf("Expected status closed, got %v", closed.Status)
	}

	t.Log("✅ All example operations work correctly")
}

// TestDependencyConstants ensures all constants are accessible
func TestDependencyConstants(t *testing.T) {
	// Test that all constants from the example are accessible
	_ = beads.StatusOpen
	_ = beads.StatusInProgress
	_ = beads.StatusClosed
	_ = beads.StatusBlocked

	_ = beads.TypeBug
	_ = beads.TypeFeature
	_ = beads.TypeTask
	_ = beads.TypeEpic
	_ = beads.TypeChore

	_ = beads.DepBlocks
	_ = beads.DepRelated
	_ = beads.DepParentChild
	_ = beads.DepDiscoveredFrom
}

// TestFindDatabasePath tests database discovery
func TestFindDatabasePath(t *testing.T) {
	// BEADS_DIR and BEADS_DB are consulted before the directory search, so
	// clear them: an ambient workspace must not answer for this one. The
	// server-mode switches would route discovery to .beads/dolt, so clear
	// those too.
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	clearServerModeEnv(t)

	// Lay out the embedded Dolt workspace bd init creates by default.
	// Discovery is Dolt-only: a bare .beads/*.db file (the removed SQLite
	// backend's layout) is not a database.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatalf("Failed to create embedded Dolt dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Change to temp directory
	t.Chdir(tmpDir)

	// Test FindDatabasePath
	found := beads.FindDatabasePath()
	if found == "" {
		t.Fatal("Expected to find database, got empty string")
	}

	// The result is canonicalized (temp dirs are symlinked on macOS), so
	// compare by file identity rather than by string.
	foundInfo, err := os.Stat(found)
	if err != nil {
		t.Fatalf("Failed to stat found database %q: %v", found, err)
	}
	wantInfo, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Failed to stat embedded Dolt dir: %v", err)
	}
	if !os.SameFile(foundInfo, wantInfo) {
		t.Errorf("FindDatabasePath() = %q, want %q", found, dataDir)
	}

	t.Logf("Found database: %s", found)
}
