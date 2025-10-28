package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

		// Should pass validation
		err := validatePreExport(ctx, store, jsonlPath)
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
