package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

const testActor = "test"

// TestJSONLIntegrityValidation tests the JSONL integrity validation (bd-160)
func TestJSONLIntegrityValidation(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")
	
	// Ensure .beads directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	
	// Create database
	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer testStore.Close()
	
	// Set global store for validateJSONLIntegrity
	oldStore := store
	store = testStore
	defer func() { store = oldStore }()
	
	ctx := context.Background()
	
	// Initialize database with prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue prefix: %v", err)
	}
	
	// Create a test issue
	issue := &types.Issue{
		ID:          "bd-1",
		Title:       "Test issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	
	if err := testStore.CreateIssue(ctx, issue, testActor); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	
	// Export to JSONL
	issues := []*types.Issue{issue}
	exportedIDs, err := writeJSONLAtomic(jsonlPath, issues)
	if err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}
	
	if len(exportedIDs) != 1 {
		t.Fatalf("expected 1 exported ID, got %d", len(exportedIDs))
	}
	
	// Compute and store JSONL file hash
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
	
	// Test 1: Validate with matching hash (should succeed)
	t.Run("MatchingHash", func(t *testing.T) {
		needsFullExport, err := validateJSONLIntegrity(ctx, jsonlPath)
		if err != nil {
			t.Fatalf("validation failed with matching hash: %v", err)
		}
		if needsFullExport {
			t.Fatalf("expected needsFullExport=false for matching hash")
		}
	})
	
	// Test 2: Modify JSONL file (simulating git pull) and validate
	t.Run("MismatchedHash", func(t *testing.T) {
		// Modify the JSONL file
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"bd-1","title":"Modified"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to modify JSONL: %v", err)
		}
		
		// Add an export hash to verify it gets cleared
		if err := testStore.SetExportHash(ctx, "bd-1", "dummy-hash"); err != nil {
			t.Fatalf("failed to set export hash: %v", err)
		}
		
		// Validate should detect mismatch and clear export_hashes
		needsFullExport, err := validateJSONLIntegrity(ctx, jsonlPath)
		if err != nil {
			t.Fatalf("validation failed: %v", err)
		}
		if !needsFullExport {
			t.Fatalf("expected needsFullExport=true after clearing export_hashes")
		}
		
		// Verify export_hashes were cleared
		hash, err := testStore.GetExportHash(ctx, "bd-1")
		if err != nil {
			t.Fatalf("failed to get export hash: %v", err)
		}
		if hash != "" {
			t.Fatalf("expected export hash to be cleared, got %q", hash)
		}
	})
	
	// Test 3: Missing JSONL file
	t.Run("MissingJSONL", func(t *testing.T) {
		// Store a hash to simulate previous export
		if err := testStore.SetJSONLFileHash(ctx, "some-hash"); err != nil {
			t.Fatalf("failed to set JSONL file hash: %v", err)
		}
		
		// Add an export hash
		if err := testStore.SetExportHash(ctx, "bd-1", "dummy-hash"); err != nil {
			t.Fatalf("failed to set export hash: %v", err)
		}
		
		// Remove JSONL file
		if err := os.Remove(jsonlPath); err != nil {
			t.Fatalf("failed to remove JSONL: %v", err)
		}
		
		// Validate should detect missing file and clear export_hashes
		needsFullExport, err := validateJSONLIntegrity(ctx, jsonlPath)
		if err != nil {
			t.Fatalf("validation failed: %v", err)
		}
		if !needsFullExport {
			t.Fatalf("expected needsFullExport=true after clearing export_hashes")
		}
		
		// Verify export_hashes were cleared
		hash, err := testStore.GetExportHash(ctx, "bd-1")
		if err != nil {
			t.Fatalf("failed to get export hash: %v", err)
		}
		if hash != "" {
			t.Fatalf("expected export hash to be cleared, got %q", hash)
		}
	})
}

// TestImportClearsExportHashes tests that imports clear export_hashes (bd-160)
func TestImportClearsExportHashes(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	// Ensure .beads directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	
	// Create database
	testStore, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer testStore.Close()
	
	ctx := context.Background()
	
	// Initialize database with prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue prefix: %v", err)
	}
	
	// Create a test issue
	issue := &types.Issue{
		ID:          "bd-1",
		Title:       "Test issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	
	if err := testStore.CreateIssue(ctx, issue, testActor); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	
	// Set an export hash
	if err := testStore.SetExportHash(ctx, "bd-1", "dummy-hash"); err != nil {
		t.Fatalf("failed to set export hash: %v", err)
	}
	
	// Verify hash is set
	hash, err := testStore.GetExportHash(ctx, "bd-1")
	if err != nil {
		t.Fatalf("failed to get export hash: %v", err)
	}
	if hash != "dummy-hash" {
		t.Fatalf("expected hash 'dummy-hash', got %q", hash)
	}
	
	// Import another issue (should clear export_hashes)
	issue2 := &types.Issue{
		ID:          "bd-2",
		Title:       "Another issue",
		Description: "Another description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	
	opts := ImportOptions{

		DryRun:               false,
		SkipUpdate:           false,
		Strict:               false,
		SkipPrefixValidation: true,
	}
	
	_, err = importIssuesCore(ctx, dbPath, testStore, []*types.Issue{issue2}, opts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	
	// Verify export_hashes were cleared
	hash, err = testStore.GetExportHash(ctx, "bd-1")
	if err != nil {
		t.Fatalf("failed to get export hash after import: %v", err)
	}
	if hash != "" {
		t.Fatalf("expected export hash to be cleared after import, got %q", hash)
	}
}
