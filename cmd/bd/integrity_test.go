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

func TestValidatePreExport(t *testing.T) {
	ctx := context.Background()

	t.Run("empty DB over non-empty JSONL fails", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create empty database
		store := newTestStore(t, dbPath)

		// Create non-empty JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Should fail validation
		err := validatePreExport(ctx, store, jsonlPath)
		if err == nil {
			t.Error("Expected error for empty DB over non-empty JSONL, got nil")
		}
	})

	t.Run("non-empty DB over non-empty JSONL succeeds", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create database with issues
		store := newTestStoreWithPrefix(t, dbPath, "bd")

		// Add an issue
		ctx := context.Background()
		issue := &types.Issue{
			ID:          "bd-1",
			Title:       "Test",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue",
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Create JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Store hash metadata to indicate JSONL and DB are in sync
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := store.SetMetadata(ctx, "last_import_hash", hash); err != nil {
			t.Fatalf("Failed to set hash metadata: %v", err)
		}

		// Should pass validation
		err = validatePreExport(ctx, store, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("empty DB over missing JSONL succeeds", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create empty database
		store := newTestStore(t, dbPath)

		// JSONL doesn't exist

		// Should pass validation (new repo scenario)
		err := validatePreExport(ctx, store, jsonlPath)
		if err != nil {
			t.Errorf("Expected no error for empty DB with no JSONL, got: %v", err)
		}
	})

	t.Run("empty DB over unreadable JSONL fails", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create empty database
		store := newTestStore(t, dbPath)

		// Create corrupt/unreadable JSONL file with content
		corruptContent := `{"id":"bd-1","title":INVALID JSON`
		if err := os.WriteFile(jsonlPath, []byte(corruptContent), 0600); err != nil {
			t.Fatalf("Failed to write corrupt JSONL: %v", err)
		}

		// Should fail validation (can't verify JSONL content, DB is empty, file has content)
		err := validatePreExport(ctx, store, jsonlPath)
		if err == nil {
			t.Error("Expected error for empty DB over unreadable non-empty JSONL, got nil")
		}
	})

	t.Run("JSONL content changed fails", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("Failed to create .beads dir: %v", err)
		}
		dbPath := filepath.Join(beadsDir, "beads.db")
		jsonlPath := filepath.Join(beadsDir, "beads.jsonl")

		// Create database with issue
		store := newTestStoreWithPrefix(t, dbPath, "bd")
		ctx := context.Background()
		issue := &types.Issue{
			ID:          "bd-1",
			Title:       "Test",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue",
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Create initial JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Store hash of original content
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := store.SetMetadata(ctx, "last_import_hash", hash); err != nil {
			t.Fatalf("Failed to set hash: %v", err)
		}

		// Modify JSONL content (simulates git pull that changed JSONL)
		modifiedContent := `{"id":"bd-1","title":"Modified","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(modifiedContent), 0600); err != nil {
			t.Fatalf("Failed to write modified JSONL: %v", err)
		}

		// Should fail validation (JSONL content changed, must import first)
		err = validatePreExport(ctx, store, jsonlPath)
		if err == nil {
			t.Error("Expected error for changed JSONL content, got nil")
		}
		if err != nil && !strings.Contains(err.Error(), "JSONL content has changed") {
			t.Errorf("Expected 'JSONL content has changed' error, got: %v", err)
		}
	})
}

func TestValidatePostImport(t *testing.T) {
	t.Run("issue count decreased fails", func(t *testing.T) {
		err := validatePostImport(10, 5)
		if err == nil {
			t.Error("Expected error for decreased issue count, got nil")
		}
	})

	t.Run("issue count same succeeds", func(t *testing.T) {
		err := validatePostImport(10, 10)
		if err != nil {
			t.Errorf("Expected no error for same count, got: %v", err)
		}
	})

	t.Run("issue count increased succeeds", func(t *testing.T) {
		err := validatePostImport(10, 15)
		if err != nil {
			t.Errorf("Expected no error for increased count, got: %v", err)
		}
	})
}

func TestCountDBIssues(t *testing.T) {
	t.Run("count issues in database", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")

		// Create database
		store := newTestStoreWithPrefix(t, dbPath, "bd")

		ctx := context.Background()
		// Initially 0
		count, err := countDBIssues(ctx, store)
		if err != nil {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if count != 0 {
			t.Errorf("Expected 0 issues, got %d", count)
		}

		// Add issues
		for i := 1; i <= 3; i++ {
			issue := &types.Issue{
				ID:          "bd-" + string(rune('0'+i)),
				Title:       "Test",
				Status:      types.StatusOpen,
				Priority:    1,
				IssueType:   types.TypeTask,
				Description: "Test issue",
			}
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("Failed to create issue: %v", err)
			}
		}

		// Should be 3
		count, err = countDBIssues(ctx, store)
		if err != nil {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if count != 3 {
			t.Errorf("Expected 3 issues, got %d", count)
		}
	})
}

func TestHasJSONLChanged(t *testing.T) {
	ctx := context.Background()

	t.Run("hash matches - no change", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create database
		store := newTestStore(t, dbPath)

		// Create JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Compute hash and store it
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := store.SetMetadata(ctx, "last_import_hash", hash); err != nil {
			t.Fatalf("Failed to set metadata: %v", err)
		}

		// Store mtime for fast-path
		if info, err := os.Stat(jsonlPath); err == nil {
			mtimeStr := fmt.Sprintf("%d", info.ModTime().Unix())
			if err := store.SetMetadata(ctx, "last_import_mtime", mtimeStr); err != nil {
				t.Fatalf("Failed to set mtime: %v", err)
			}
		}

		// Should return false (no change)
		if hasJSONLChanged(ctx, store, jsonlPath, "") {
			t.Error("Expected hasJSONLChanged to return false for matching hash")
		}
	})

	t.Run("hash differs - has changed", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create database
		store := newTestStore(t, dbPath)

		// Create initial JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// Compute hash and store it
		hash, err := computeJSONLHash(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to compute hash: %v", err)
		}
		if err := store.SetMetadata(ctx, "last_import_hash", hash); err != nil {
			t.Fatalf("Failed to set metadata: %v", err)
		}

		// Modify JSONL file
		newContent := `{"id":"bd-1","title":"Modified","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(newContent), 0600); err != nil {
			t.Fatalf("Failed to write modified JSONL: %v", err)
		}

		// Should return true (content changed)
		if !hasJSONLChanged(ctx, store, jsonlPath, "") {
			t.Error("Expected hasJSONLChanged to return true for different hash")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create database
		store := newTestStore(t, dbPath)

		// Create empty JSONL file
		if err := os.WriteFile(jsonlPath, []byte(""), 0600); err != nil {
			t.Fatalf("Failed to write empty JSONL: %v", err)
		}

		// Should return true (no previous hash, first run)
		if !hasJSONLChanged(ctx, store, jsonlPath, "") {
			t.Error("Expected hasJSONLChanged to return true for empty file with no metadata")
		}
	})

	t.Run("missing metadata - first run", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create database
		store := newTestStore(t, dbPath)

		// Create JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
`
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
			t.Fatalf("Failed to write JSONL: %v", err)
		}

		// No metadata stored - should return true (assume changed)
		if !hasJSONLChanged(ctx, store, jsonlPath, "") {
			t.Error("Expected hasJSONLChanged to return true when no metadata exists")
		}
	})

	t.Run("file read error", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "nonexistent.jsonl")

		// Create database
		store := newTestStore(t, dbPath)

		// File doesn't exist - should return false (don't auto-import broken files)
		if hasJSONLChanged(ctx, store, jsonlPath, "") {
			t.Error("Expected hasJSONLChanged to return false for nonexistent file")
		}
	})

	t.Run("mtime fast-path - unchanged", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create database
		store := newTestStore(t, dbPath)

		// Create JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
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
		if err := store.SetMetadata(ctx, "last_import_hash", hash); err != nil {
			t.Fatalf("Failed to set hash: %v", err)
		}

		mtimeStr := fmt.Sprintf("%d", info.ModTime().Unix())
		if err := store.SetMetadata(ctx, "last_import_mtime", mtimeStr); err != nil {
			t.Fatalf("Failed to set mtime: %v", err)
		}

		// Should return false using fast-path (mtime unchanged)
		if hasJSONLChanged(ctx, store, jsonlPath, "") {
			t.Error("Expected hasJSONLChanged to return false using mtime fast-path")
		}
	})

	t.Run("mtime changed but content same - git operation scenario", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create database
		store := newTestStore(t, dbPath)

		// Create JSONL file
		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
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
		if err := store.SetMetadata(ctx, "last_import_hash", hash); err != nil {
			t.Fatalf("Failed to set hash: %v", err)
		}

		oldMtime := fmt.Sprintf("%d", initialInfo.ModTime().Unix()-1000) // Old mtime
		if err := store.SetMetadata(ctx, "last_import_mtime", oldMtime); err != nil {
			t.Fatalf("Failed to set old mtime: %v", err)
		}

		// Touch file to simulate git operation (new mtime, same content)
		time.Sleep(10 * time.Millisecond) // Ensure time passes
		futureTime := time.Now().Add(1 * time.Second)
		if err := os.Chtimes(jsonlPath, futureTime, futureTime); err != nil {
			t.Fatalf("Failed to touch JSONL: %v", err)
		}

		// Should return false (content hasn't changed despite new mtime)
		if hasJSONLChanged(ctx, store, jsonlPath, "") {
			t.Error("Expected hasJSONLChanged to return false for git operation with same content")
		}
	})
}

func TestComputeJSONLHash(t *testing.T) {
	t.Run("computes hash correctly", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
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

		jsonlContent := `{"id":"bd-1","title":"Test","status":"open","priority":1}
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

		if err := os.WriteFile(jsonlPath1, []byte(`{"id":"bd-1"}`), 0600); err != nil {
			t.Fatalf("Failed to write JSONL 1: %v", err)
		}
		if err := os.WriteFile(jsonlPath2, []byte(`{"id":"bd-2"}`), 0600); err != nil {
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

func TestCheckOrphanedDeps(t *testing.T) {
	t.Run("function executes without error", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")

		// Create database
		store := newTestStoreWithPrefix(t, dbPath, "bd")

		ctx := context.Background()
		// Create two issues
		issue1 := &types.Issue{
			ID:          "bd-1",
			Title:       "Test 1",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 1",
		}
		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue 1: %v", err)
		}

		issue2 := &types.Issue{
			ID:          "bd-2",
			Title:       "Test 2",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 2",
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue 2: %v", err)
		}

		// Add dependency
		dep := &types.Dependency{
			IssueID:     "bd-1",
			DependsOnID: "bd-2",
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		// Check for orphaned deps - should succeed without error
		// Note: Database maintains referential integrity, so we can't easily create orphaned deps in tests
		// This test verifies the function executes correctly
		orphaned, err := checkOrphanedDeps(ctx, store)
		if err != nil {
			t.Fatalf("Failed to check orphaned deps: %v", err)
		}

		// With proper foreign keys, there should be no orphaned dependencies
		if len(orphaned) != 0 {
			t.Logf("Note: Found %d orphaned dependencies (unexpected with FK constraints): %v", len(orphaned), orphaned)
		}
	})

	t.Run("no orphaned dependencies", func(t *testing.T) {
		// Create temp directory
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")

		// Create database
		store := newTestStoreWithPrefix(t, dbPath, "bd")

		ctx := context.Background()
		// Create two issues
		issue1 := &types.Issue{
			ID:          "bd-1",
			Title:       "Test 1",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 1",
		}
		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue 1: %v", err)
		}

		issue2 := &types.Issue{
			ID:          "bd-2",
			Title:       "Test 2",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			Description: "Test issue 2",
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue 2: %v", err)
		}

		// Add valid dependency
		dep := &types.Dependency{
			IssueID:     "bd-1",
			DependsOnID: "bd-2",
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		// Check for orphaned deps
		orphaned, err := checkOrphanedDeps(ctx, store)
		if err != nil {
			t.Fatalf("Failed to check orphaned deps: %v", err)
		}

		if len(orphaned) != 0 {
			t.Errorf("Expected 0 orphaned dependencies, got %d: %v", len(orphaned), orphaned)
		}
	})
}
