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

// TestContentBasedComparison verifies that isJSONLNewer uses content comparison
// instead of just timestamp comparison to prevent false positives (bd-lm2q)
func TestContentBasedComparison(t *testing.T) {
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
	localStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer localStore.Close()

	ctx := context.Background()

	// Initialize database with issue_prefix
	if err := localStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
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
	if err := localStore.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	if err := exportToJSONLWithStore(ctx, localStore, jsonlPath); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Touch database to match JSONL
	if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
		t.Fatalf("TouchDatabaseFile failed: %v", err)
	}

	// Verify they're in sync (content matches, timestamps match)
	if isJSONLNewerWithStore(jsonlPath, localStore) {
		t.Error("isJSONLNewer should return false when content matches (before timestamp manipulation)")
	}

	// Wait to ensure timestamp difference
	time.Sleep(100 * time.Millisecond)

	// Simulate daemon auto-export: touch JSONL to make it newer
	// This simulates clock skew or filesystem timestamp quirks
	futureTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(jsonlPath, futureTime, futureTime); err != nil {
		t.Fatalf("Failed to update JSONL timestamp: %v", err)
	}

	// Verify JSONL is newer by timestamp
	jsonlInfo, _ := os.Stat(jsonlPath)
	dbInfo, _ := os.Stat(dbPath)
	if !jsonlInfo.ModTime().After(dbInfo.ModTime()) {
		t.Fatal("Test setup failed: JSONL should be newer than DB by timestamp")
	}

	// KEY TEST: isJSONLNewer should return FALSE because content is identical
	// despite timestamp difference (this is the bd-lm2q fix)

	// Compute hashes for debugging
	jsonlHash1, err := computeJSONLHash(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to compute JSONL hash: %v", err)
	}
	dbHash1, err := computeDBHash(ctx, localStore)
	if err != nil {
		t.Fatalf("Failed to compute DB hash: %v", err)
	}
	t.Logf("JSONL hash: %s", jsonlHash1)
	t.Logf("DB hash:    %s", dbHash1)

	if isJSONLNewerWithStore(jsonlPath, localStore) {
		t.Error("isJSONLNewer should return false when content matches despite timestamp difference (bd-lm2q)")
		t.Logf("Hashes: JSONL=%s, DB=%s", jsonlHash1, dbHash1)
	}

	// Now modify the database (add a new issue)
	issue2 := &types.Issue{
		ID:        "test-2",
		Title:     "New Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	if err := localStore.CreateIssue(ctx, issue2, "test-actor"); err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}

	// Re-export to JSONL to reflect new content
	if err := exportToJSONLWithStore(ctx, localStore, jsonlPath); err != nil {
		t.Fatalf("Second export failed: %v", err)
	}

	// Make JSONL appear older than DB by timestamp
	pastTime := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(jsonlPath, pastTime, pastTime); err != nil {
		t.Fatalf("Failed to update JSONL timestamp to past: %v", err)
	}

	// Verify JSONL is older by timestamp
	jsonlInfo, _ = os.Stat(jsonlPath)
	dbInfo, _ = os.Stat(dbPath)
	if jsonlInfo.ModTime().After(dbInfo.ModTime()) {
		t.Fatal("Test setup failed: JSONL should be older than DB by timestamp")
	}

	// isJSONLNewer should return false because JSONL is older by timestamp
	if isJSONLNewerWithStore(jsonlPath, localStore) {
		t.Error("isJSONLNewer should return false when JSONL is older by timestamp")
	}

	// Final test: Make JSONL newer AND different content
	// First, manually edit JSONL to have different content
	originalJSONL, err := os.ReadFile(jsonlPath) // #nosec G304 - test code
	if err != nil {
		t.Fatalf("Failed to read JSONL: %v", err)
	}

	// Remove the second issue from database
	if err := localStore.DeleteIssue(ctx, "test-2"); err != nil {
		t.Fatalf("Failed to delete issue: %v", err)
	}

	// Restore the JSONL with 2 issues (making it different from DB which has 1)
	if err := os.WriteFile(jsonlPath, originalJSONL, 0600); err != nil { // #nosec G306 - test code
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Make JSONL newer by timestamp
	if err := os.Chtimes(jsonlPath, futureTime, futureTime); err != nil {
		t.Fatalf("Failed to update JSONL timestamp: %v", err)
	}

	// Now isJSONLNewer should return TRUE (newer timestamp AND different content)
	if !isJSONLNewerWithStore(jsonlPath, localStore) {
		t.Error("isJSONLNewer should return true when JSONL is newer AND has different content")
	}
}

// TestContentHashComputation verifies the hash computation functions
func TestContentHashComputation(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create and populate database
	localStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer localStore.Close()

	ctx := context.Background()

	if err := localStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := localStore.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	if err := exportToJSONLWithStore(ctx, localStore, jsonlPath); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Compute hashes
	jsonlHash, err := computeJSONLHash(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to compute JSONL hash: %v", err)
	}

	dbHash, err := computeDBHash(ctx, localStore)
	if err != nil {
		t.Fatalf("Failed to compute DB hash: %v", err)
	}

	// Hashes should match since we just exported
	if jsonlHash != dbHash {
		t.Errorf("Hash mismatch after export:\nJSONL: %s\nDB:    %s", jsonlHash, dbHash)
	}

	// Modify database by adding a new issue
	issue2 := &types.Issue{
		ID:        "test-2",
		Title:     "Second Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	if err := localStore.CreateIssue(ctx, issue2, "test-actor"); err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}

	// Compute new DB hash
	newDBHash, err := computeDBHash(ctx, localStore)
	if err != nil {
		t.Fatalf("Failed to compute new DB hash: %v", err)
	}

	// Hashes should differ now
	if jsonlHash == newDBHash {
		t.Error("Hash should differ after DB modification")
	}

	// Hash should be consistent across multiple calls
	dbHash2, err := computeDBHash(ctx, localStore)
	if err != nil {
		t.Fatalf("Failed to compute DB hash second time: %v", err)
	}

	if newDBHash != dbHash2 {
		t.Error("DB hash should be consistent across calls")
	}
}
