package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestExportIntegrityAfterJSONLTruncation simulates the bd-160 bug scenario.
// This integration test would have caught the export deduplication bug.
func TestExportIntegrityAfterJSONLTruncation(t *testing.T) {
	// Setup: Create a database with multiple issues
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	// Initialize database
	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue prefix: %v", err)
	}

	// Create 10 issues
	const numIssues = 10
	var allIssues []*types.Issue
	for i := 1; i <= numIssues; i++ {
		issue := &types.Issue{
			ID:          "bd-" + strconv.Itoa(i),
			Title:       "Test issue " + strconv.Itoa(i),
			Description: "Description " + strconv.Itoa(i),
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		}
		allIssues = append(allIssues, issue)

		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Step 1: Export all issues
	exportedIDs, err := writeJSONLAtomic(jsonlPath, allIssues)
	if err != nil {
		t.Fatalf("initial export failed: %v", err)
	}

	if len(exportedIDs) != numIssues {
		t.Fatalf("expected %d exported issues, got %d", numIssues, len(exportedIDs))
	}

	// Store JSONL file hash (simulating what the system should do)
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}

	// Compute and store the JSONL file hash
	hasher := sha256.New()
	hasher.Write(jsonlData)
	fileHash := hex.EncodeToString(hasher.Sum(nil))
	if err := testStore.SetJSONLFileHash(ctx, fileHash); err != nil {
		t.Fatalf("failed to set JSONL file hash: %v", err)
	}

	initialSize := len(jsonlData)

	// Step 2: Simulate git operation that truncates JSONL (the bd-160 scenario)
	// This simulates: git reset --hard <old-commit>, git checkout <branch>, etc.
	truncatedData := jsonlData[:len(jsonlData)/2] // Keep only first half
	if err := os.WriteFile(jsonlPath, truncatedData, 0644); err != nil {
		t.Fatalf("failed to truncate JSONL: %v", err)
	}

	// Verify JSONL is indeed truncated
	truncatedSize := len(truncatedData)
	if truncatedSize >= initialSize {
		t.Fatalf("JSONL should be truncated, but size is %d (was %d)", truncatedSize, initialSize)
	}

	// Step 3: Run export again with integrity validation enabled
	// Set global store for validateJSONLIntegrity
	oldStore := store
	store = testStore
	defer func() { store = oldStore }()

	// This should detect the mismatch and clear export_hashes
	needsFullExport, err := validateJSONLIntegrity(ctx, jsonlPath)
	if err != nil {
		t.Fatalf("integrity validation failed: %v", err)
	}
	if !needsFullExport {
		t.Fatalf("expected needsFullExport=true after truncation")
	}

	// Step 4: Export all issues again
	exportedIDs2, err := writeJSONLAtomic(jsonlPath, allIssues)
	if err != nil {
		t.Fatalf("second export failed: %v", err)
	}

	// Step 5: Verify all issues were exported (not skipped)
	if len(exportedIDs2) != numIssues {
		t.Errorf("INTEGRITY VIOLATION: expected %d exported issues after truncation, got %d",
			numIssues, len(exportedIDs2))
		t.Errorf("This indicates the bug bd-160 would have occurred!")

		// Read JSONL to count actual lines
		finalData, _ := os.ReadFile(jsonlPath)
		lines := 0
		for _, b := range finalData {
			if b == '\n' {
				lines++
			}
		}
		t.Errorf("JSONL has %d lines, DB has %d issues", lines, numIssues)
	}

	// Step 6: Verify JSONL has all issues
	finalData, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read final JSONL: %v", err)
	}

	// Count newlines to verify all issues present
	lineCount := 0
	for _, b := range finalData {
		if b == '\n' {
			lineCount++
		}
	}

	if lineCount != numIssues {
		t.Errorf("JSONL should have %d lines (issues), got %d", numIssues, lineCount)
		t.Errorf("Data loss detected - this is the bd-160 bug!")
	}
}

// TestExportIntegrityAfterJSONLDeletion tests recovery when JSONL is deleted
func TestExportIntegrityAfterJSONLDeletion(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue prefix: %v", err)
	}

	// Create issues and export
	issue := &types.Issue{
		ID:        "bd-1",
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	_, err = writeJSONLAtomic(jsonlPath, []*types.Issue{issue})
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Store JSONL hash (would happen in real export)
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}
	hasher := sha256.New()
	hasher.Write(jsonlData)
	fileHash := hex.EncodeToString(hasher.Sum(nil))
	if err := testStore.SetJSONLFileHash(ctx, fileHash); err != nil {
		t.Fatalf("failed to set JSONL file hash: %v", err)
	}

	// Set global store
	oldStore := store
	store = testStore
	defer func() { store = oldStore }()

	// Delete JSONL (simulating user error or git clean)
	if err := os.Remove(jsonlPath); err != nil {
		t.Fatalf("failed to remove JSONL: %v", err)
	}

	// Integrity validation should detect missing file
	// (In real system, this happens before next export)
	needsFullExport, err := validateJSONLIntegrity(ctx, jsonlPath)
	if err != nil {
		// Error is OK if file doesn't exist
		if !os.IsNotExist(err) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if !needsFullExport {
		t.Fatalf("expected needsFullExport=true after JSONL deletion")
	}

	// Export again should recreate JSONL
	_, err = writeJSONLAtomic(jsonlPath, []*types.Issue{issue})
	if err != nil {
		t.Fatalf("export after deletion failed: %v", err)
	}

	// Verify JSONL was recreated
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("JSONL should have been recreated")
	}

	// Verify content
	newData, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read recreated JSONL: %v", err)
	}

	if len(newData) == 0 {
		t.Fatal("Recreated JSONL is empty - data loss!")
	}
}

// TestMultipleExportsStayConsistent tests that repeated exports maintain integrity
func TestMultipleExportsStayConsistent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue prefix: %v", err)
	}

	// Create 5 issues
	var issues []*types.Issue
	for i := 1; i <= 5; i++ {
		issue := &types.Issue{
			ID:        "bd-" + strconv.Itoa(i),
			Title:     "Issue " + strconv.Itoa(i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		issues = append(issues, issue)
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Export multiple times and verify consistency
	for iteration := 0; iteration < 3; iteration++ {
		exportedIDs, err := writeJSONLAtomic(jsonlPath, issues)
		if err != nil {
			t.Fatalf("export iteration %d failed: %v", iteration, err)
		}

		if len(exportedIDs) != len(issues) {
			t.Errorf("iteration %d: expected %d exports, got %d",
				iteration, len(issues), len(exportedIDs))
		}

		// Count lines in JSONL
		data, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("failed to read JSONL: %v", err)
		}

		lines := 0
		for _, b := range data {
			if b == '\n' {
				lines++
			}
		}

		if lines != len(issues) {
			t.Errorf("iteration %d: JSONL has %d lines, expected %d",
				iteration, lines, len(issues))
		}
	}
}
