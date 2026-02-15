//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestValidatePreExportSuite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, dbPath)
	ctx := context.Background()

	t.Run("empty DB over non-empty JSONL fails", func(t *testing.T) {
		// Create temp directory for JSONL
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")

		// Create non-empty JSONL file
		jsonlContent := `{"id":"test-v1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Should fail validation
		err := validatePreExport(ctx, s, jsonlPath)
		if err == nil {
			t.Error("Expected error for empty DB over non-empty JSONL, got nil")
		}
	})

	t.Run("non-empty DB over non-empty JSONL succeeds", func(t *testing.T) {
		// Create temp directory for JSONL
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")

		// Add an issue with unique ID prefix
		issue := &types.Issue{
			ID:          "test-v2",
			Title:       "Test",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Create JSONL file
		jsonlContent := `{"id":"test-v2","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Store hash metadata to indicate JSONL and DB are in sync
		// bd-39o: renamed from last_import_hash to jsonl_content_hash
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := s.SetMetadata(ctx, "jsonl_content_hash", hash); err != nil {
			t.Fatalf("Failed to set hash metadata: %v", err)
		}

		// Should pass validation
		err = validatePreExport(ctx, s, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("empty DB over missing JSONL succeeds", func(t *testing.T) {
		// Create temp directory for JSONL
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")

		// JSONL doesn't exist

		// Should pass validation (new repo scenario)
		err := validatePreExport(ctx, s, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error for empty DB with no JSONL, got: %v", err)
		}
	})

	t.Run("empty DB over unreadable JSONL fails", func(t *testing.T) {
		// Create temp directory for JSONL
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")

		// Create corrupt/unreadable JSONL file with content
		corruptContent := `{"id":"test-v4","title":INVALID JSON`
		if err := os.WriteFile(jsonlPath, []byte(corruptContent), 0600); err != nil {
			t.Fatalf("Failed to write corrupt JSONL: %v", err)
		}

		// Should fail validation (can't verify JSONL content, DB is empty, file has content)
		err := validatePreExport(ctx, s, jsonlPath)
		if err == nil {
			t.Error("Expected error for empty DB over unreadable non-empty JSONL, got nil")
		}
	})

	t.Run("JSONL content changed fails", func(t *testing.T) {
		// Create temp directory for JSONL
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")

		// Create issue with unique ID prefix
		issue := &types.Issue{
			ID:          "test-v5",
			Title:       "Test",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Create initial JSONL file
		jsonlContent := `{"id":"test-v5","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Store hash of original content (bd-39o: renamed to jsonl_content_hash)
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := s.SetMetadata(ctx, "jsonl_content_hash", hash); err != nil {
			t.Fatalf("Failed to set hash: %v", err)
		}

		// Modify JSONL content (simulates git pull that changed JSONL)
		modifiedContent := `{"id":"test-v5","title":"Modified","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(modifiedContent), 0600); err != nil {
			t.Fatalf("Failed to write modified JSONL: %v", err)
		}

		// Should fail validation (JSONL content changed, must import first)
		err = validatePreExport(ctx, s, jsonlPath)
		if err == nil {
			t.Error("Expected error for changed JSONL content, got nil")
		}
		if err != nil && !strings.Contains(err.Error(), "JSONL content has changed") {
			t.Errorf("Expected 'JSONL content has changed' error, got: %v", err)
		}
	})
}

func TestValidatePostImport(t *testing.T) {
	// Note: validatePostImport no longer fails on decreases - it only warns.
	// The deletions.jsonl validation has been removed.

	t.Run("issue count decreased warns but succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		// Decreases are allowed (just warn)
		err := validatePostImport(10, 5, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error (just warning) for decreased count, got: %v", err)
		}
	})

	t.Run("issue count same succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		err := validatePostImport(10, 10, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error for same count, got: %v", err)
		}
	})

	t.Run("issue count increased succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		err := validatePostImport(10, 15, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error for increased count, got: %v", err)
		}
	})
}

func TestValidatePostImportWithExpectedDeletions(t *testing.T) {
	// Note: validatePostImportWithExpectedDeletions no longer fails on decreases -
	// it only warns. The deletions.jsonl validation has been removed.

	t.Run("decrease fully accounted for by expected deletions succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		err := validatePostImportWithExpectedDeletions(26, 25, 1, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error when decrease matches expected deletions, got: %v", err)
		}
	})

	t.Run("decrease exceeds expected deletions warns but succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		// Decrease of 5, expected 2 - used to fail, now warns
		err := validatePostImportWithExpectedDeletions(20, 15, 2, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error (just warning) for decreased count, got: %v", err)
		}
	})

	t.Run("no decrease succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		err := validatePostImportWithExpectedDeletions(10, 10, 5, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error for same count, got: %v", err)
		}
	})

	t.Run("increase succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		err := validatePostImportWithExpectedDeletions(10, 15, 0, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error for increased count, got: %v", err)
		}
	})
}

func TestCountDBIssuesSuite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s := newTestStoreWithPrefix(t, dbPath, "test")
	ctx := context.Background()

	t.Run("count issues in database", func(t *testing.T) {
		// Initially 0
		count, err := countDBIssues(ctx, s)
		if err != nil {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if count != 0 {
			t.Errorf("Expected 0 issues, got %d", count)
		}

		// Add issues with unique IDs
		for i := 1; i <= 3; i++ {
			issue := &types.Issue{
				ID:          fmt.Sprintf("test-count-%d", i),
				Title:       "Test",
				Status:      types.StatusOpen,
				Priority:    1,
				IssueType:   types.TypeTask,
				Description: "Test issue",
			}
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("Failed to create issue: %v", err)
			}
		}

		// Should be 3
		count, err = countDBIssues(ctx, s)
		if err != nil {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if count != 3 {
			t.Errorf("Expected 3 issues, got %d", count)
		}
	})
}

func TestHasJSONLChangedSuite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, dbPath)
	ctx := context.Background()

	t.Run("hash matches - no change", func(t *testing.T) {
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")
		keySuffix := "h1"

		// Create JSONL file
		jsonlContent := `{"id":"test-h1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Compute hash and store it
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := s.SetMetadata(ctx, "jsonl_content_hash:"+keySuffix, hash); err != nil {
			t.Fatalf("Failed to set metadata: %v", err)
		}

		// Store mtime for fast-path
		if info, err := os.Stat(jsonlPath); err == nil {
			mtimeStr := fmt.Sprintf("%d", info.ModTime().Unix())
			if err := s.SetMetadata(ctx, "last_import_mtime:"+keySuffix, mtimeStr); err != nil {
				t.Fatalf("Failed to set mtime: %v", err)
			}
		}

		// Should return false (no change)
		if hasJSONLChanged(ctx, s, jsonlPath, keySuffix) {
			t.Error("Expected hasJSONLChanged to return false for matching hash")
		}
	})

	t.Run("hash differs - has changed", func(t *testing.T) {
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")
		keySuffix := "h2"

		// Create initial JSONL file
		jsonlContent := `{"id":"test-h2","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Compute hash and store it
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := s.SetMetadata(ctx, "jsonl_content_hash:"+keySuffix, hash); err != nil {
			t.Fatalf("Failed to set metadata: %v", err)
		}

		// Modify JSONL file
		newContent := `{"id":"test-h2","title":"Modified","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(newContent), 0600); err != nil {
			t.Fatalf("Failed to write modified JSONL: %v", err)
		}

		// Should return true (content changed)
		if !hasJSONLChanged(ctx, s, jsonlPath, keySuffix) {
			t.Error("Expected hasJSONLChanged to return true for different hash")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")
		keySuffix := "h3"

		// Create empty JSONL file
		if err := os.WriteFile(jsonlPath, []byte(""), 0600); err != nil {
			t.Fatalf("Failed to write empty JSONL: %v", err)
		}

		// Should return true (no previous hash, first run)
		if !hasJSONLChanged(ctx, s, jsonlPath, keySuffix) {
			t.Error("Expected hasJSONLChanged to return true for empty file with no metadata")
		}
	})

	t.Run("missing metadata - first run", func(t *testing.T) {
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")
		keySuffix := "h4"

		// Create JSONL file
		jsonlContent := `{"id":"test-h4","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// No metadata stored - should return true (assume changed)
		if !hasJSONLChanged(ctx, s, jsonlPath, keySuffix) {
			t.Error("Expected hasJSONLChanged to return true when no metadata exists")
		}
	})

	t.Run("file read error", func(t *testing.T) {
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "nonexistent.jsonl")
		keySuffix := "h5"

		// File doesn't exist - should return false (don't auto-import broken files)
		if hasJSONLChanged(ctx, s, jsonlPath, keySuffix) {
			t.Error("Expected hasJSONLChanged to return false for nonexistent file")
		}
	})

	t.Run("mtime fast-path - unchanged", func(t *testing.T) {
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")
		keySuffix := "h6"

		// Create JSONL file
		jsonlContent := `{"id":"test-h6","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Get file info
		info, err := os.Stat(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to stat JSONL: %v", err)
		}

		// Store hash and mtime
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := s.SetMetadata(ctx, "jsonl_content_hash:"+keySuffix, hash); err != nil {
			t.Fatalf("Failed to set hash: %v", err)
		}

		mtimeStr := fmt.Sprintf("%d", info.ModTime().Unix())
		if err := s.SetMetadata(ctx, "last_import_mtime:"+keySuffix, mtimeStr); err != nil {
			t.Fatalf("Failed to set mtime: %v", err)
		}

		// Should return false using fast-path (mtime unchanged)
		if hasJSONLChanged(ctx, s, jsonlPath, keySuffix) {
			t.Error("Expected hasJSONLChanged to return false using mtime fast-path")
		}
	})

	t.Run("mtime changed but content same - git operation scenario", func(t *testing.T) {
		jsonlTmpDir := t.TempDir()
		jsonlPath := filepath.Join(jsonlTmpDir, "issues.jsonl")
		keySuffix := "h7"

		// Create JSONL file
		jsonlContent := `{"id":"test-h7","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Get initial file info
		initialInfo, err := os.Stat(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to stat JSONL: %v", err)
		}

		// Store hash and old mtime
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := s.SetMetadata(ctx, "jsonl_content_hash:"+keySuffix, hash); err != nil {
			t.Fatalf("Failed to set hash: %v", err)
		}

		oldMtime := fmt.Sprintf("%d", initialInfo.ModTime().Unix()-1000) // Old mtime
		if err := s.SetMetadata(ctx, "last_import_mtime:"+keySuffix, oldMtime); err != nil {
			t.Fatalf("Failed to set old mtime: %v", err)
		}

		// Touch file to simulate git operation (new mtime, same content)
		time.Sleep(10 * time.Millisecond) // Ensure time passes
		futureTime := time.Now().Add(1 * time.Second)
		if err := os.Chtimes(jsonlPath, futureTime, futureTime); err != nil {
			t.Fatalf("Failed to touch JSONL: %v", err)
		}

		// Should return false (content hasn't changed despite new mtime)
		if hasJSONLChanged(ctx, s, jsonlPath, keySuffix) {
			t.Error("Expected hasJSONLChanged to return false for git operation with same content")
		}
	})
}

func TestComputeJSONLHash(t *testing.T) {
	t.Run("computes hash correctly", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		jsonlContent := `{"id":"test-ch1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}

		if hash == "" {
			t.Error("Expected non-empty hash")
		}

		if len(hash) != 64 { // SHA256 hex is 64 chars
			t.Errorf("Expected hash length 64, got %d", len(hash))
		}
	})

	t.Run("same content produces same hash", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath1 := filepath.Join(tmpDir, "issues1.jsonl")
		jsonlPath2 := filepath.Join(tmpDir, "issues2.jsonl")

		jsonlContent := `{"id":"test-ch2","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath1, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL 1: %v", err)
		}
		if err := os.WriteFile(jsonlPath2, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL 2: %v", err)
		}

		hash1, err := computeJSONLHash(jsonlPath1)
		if err != nil {
			t.Fatalf("Failed to compute hash 1: %v", err)
		}

		hash2, err := computeJSONLHash(jsonlPath2)
		if err != nil {
			t.Fatalf("Failed to compute hash 2: %v", err)
		}

		if hash1 != hash2 {
			t.Errorf("Expected same hash for same content, got %s and %s", hash1, hash2)
		}
	})

	t.Run("different content produces different hash", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath1 := filepath.Join(tmpDir, "issues1.jsonl")
		jsonlPath2 := filepath.Join(tmpDir, "issues2.jsonl")

		if err := os.WriteFile(jsonlPath1, []byte(`{"id":"test-ch3a"}`), 0600); err != nil {
			t.Fatalf("Failed to write JSONL 1: %v", err)
		}
		if err := os.WriteFile(jsonlPath2, []byte(`{"id":"test-ch3b"}`), 0600); err != nil {
			t.Fatalf("Failed to write JSONL 2: %v", err)
		}

		hash1, err := computeJSONLHash(jsonlPath1)
		if err != nil {
			t.Fatalf("Failed to compute hash 1: %v", err)
		}

		hash2, err := computeJSONLHash(jsonlPath2)
		if err != nil {
			t.Fatalf("Failed to compute hash 2: %v", err)
		}

		if hash1 == hash2 {
			t.Errorf("Expected different hashes for different content")
		}
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "nonexistent.jsonl")

		_, err := computeJSONLHash(jsonlPath)
		if err == nil {
			t.Error("Expected error for nonexistent file, got nil")
		}
	})
}

func TestCheckOrphanedDepsSuite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s := newTestStoreWithPrefix(t, dbPath, "test")
	ctx := context.Background()

	t.Run("function executes without error", func(t *testing.T) {
		// Create two issues with unique IDs
		issue1 := &types.Issue{
			ID:          "test-orphan-1",
			Title:       "Test 1",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 1",
		}
		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue 1: %v", err)
		}

		issue2 := &types.Issue{
			ID:          "test-orphan-2",
			Title:       "Test 2",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 2",
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue 2: %v", err)
		}

		// Add dependency
		dep := &types.Dependency{
			IssueID:     "test-orphan-1",
			DependsOnID: "test-orphan-2",
			Type:        types.DepBlocks,
		}
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		// Check for orphaned deps - should succeed without error
		// Note: Database maintains referential integrity, so we can't easily create orphaned deps in tests
		// This test verifies the function executes correctly
		orphaned, err := checkOrphanedDeps(ctx, s)
		if err != nil {
			t.Fatalf("Failed to check orphaned deps: %v", err)
		}

		// With proper foreign keys, there should be no orphaned dependencies
		if len(orphaned) != 0 {
			t.Logf("Note: Found %d orphaned dependencies (unexpected with FK constraints): %v", len(orphaned), orphaned)
		}
	})

	t.Run("no orphaned dependencies", func(t *testing.T) {
		// Create two issues with unique IDs
		issue1 := &types.Issue{
			ID:          "test-orphan-3",
			Title:       "Test 3",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 3",
		}
		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue 1: %v", err)
		}

		issue2 := &types.Issue{
			ID:          "test-orphan-4",
			Title:       "Test 4",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 4",
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue 2: %v", err)
		}

		// Add valid dependency
		dep := &types.Dependency{
			IssueID:     "test-orphan-3",
			DependsOnID: "test-orphan-4",
			Type:        types.DepBlocks,
		}
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		// Check for orphaned deps
		orphaned, err := checkOrphanedDeps(ctx, s)
		if err != nil {
			t.Fatalf("Failed to check orphaned deps: %v", err)
		}

		if len(orphaned) != 0 {
			t.Errorf("Expected 0 orphaned dependencies, got %d: %v", len(orphaned), orphaned)
		}
	})
}
