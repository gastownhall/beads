//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImportFromLocalJSONL(t *testing.T) {
	skipIfNoDolt(t)

	t.Run("imports issues from JSONL file", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		// Create a JSONL file with test issues
		jsonlContent := `{"id":"test-abc123","title":"First issue","type":"bug","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-def456","title":"Second issue","type":"task","status":"open","priority":3,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}

		if count != 2 {
			t.Errorf("Expected 2 issues imported, got %d", count)
		}

		// Verify issues exist in the store
		issue1, err := store.GetIssue(ctx, "test-abc123")
		if err != nil {
			t.Fatalf("Failed to get first issue: %v", err)
		}
		if issue1.Title != "First issue" {
			t.Errorf("Expected title 'First issue', got %q", issue1.Title)
		}

		issue2, err := store.GetIssue(ctx, "test-def456")
		if err != nil {
			t.Fatalf("Failed to get second issue: %v", err)
		}
		if issue2.Title != "Second issue" {
			t.Errorf("Expected title 'Second issue', got %q", issue2.Title)
		}
	})

	t.Run("empty JSONL file imports zero issues", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}

		if count != 0 {
			t.Errorf("Expected 0 issues imported from empty file, got %d", count)
		}
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		ctx := context.Background()
		_, err := importFromLocalJSONL(ctx, store, "/nonexistent/issues.jsonl")
		if err == nil {
			t.Error("Expected error for nonexistent file, got nil")
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte("not valid json\n"), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		_, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err == nil {
			t.Error("Expected error for invalid JSON, got nil")
		}
	})

	t.Run("re-import with duplicate IDs succeeds via upsert", func(t *testing.T) {
		// GH#2061: importing the same JSONL twice should not fail with
		// "duplicate primary key" — the second import should upsert.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlContent := `{"id":"test-dup1","title":"Original title","type":"bug","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-dup2","title":"Second issue","type":"task","status":"open","priority":3,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()

		// First import
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("first importFromLocalJSONL failed: %v", err)
		}
		if count != 2 {
			t.Errorf("Expected 2 issues imported on first import, got %d", count)
		}

		// Second import with same IDs — should succeed (upsert), not fail
		updatedContent := `{"id":"test-dup1","title":"Updated title","type":"bug","status":"closed","priority":1,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-06-01T00:00:00Z"}
{"id":"test-dup2","title":"Second issue","type":"task","status":"open","priority":3,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		if err := os.WriteFile(jsonlPath, []byte(updatedContent), 0644); err != nil {
			t.Fatalf("Failed to write updated JSONL file: %v", err)
		}

		count2, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("second importFromLocalJSONL failed (duplicate key?): %v", err)
		}
		if count2 != 2 {
			t.Errorf("Expected 2 issues on re-import, got %d", count2)
		}

		// Verify the first issue was updated (upsert, not just inserted)
		issue, err := store.GetIssue(ctx, "test-dup1")
		if err != nil {
			t.Fatalf("Failed to get upserted issue: %v", err)
		}
		if issue.Title != "Updated title" {
			t.Errorf("Expected title 'Updated title' after upsert, got %q", issue.Title)
		}
		if issue.Status != "closed" {
			t.Errorf("Expected status 'closed' after upsert, got %q", issue.Status)
		}
	})

	t.Run("child counter reconciled after JSONL import prevents overwrites", func(t *testing.T) {
		// Regression test for GH#2166: bd create --parent after bd init --from-jsonl
		// must not overwrite existing child issues. The child_counters table
		// must be reconciled from imported hierarchical IDs.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		// Import an epic with two existing children via JSONL
		jsonlContent := `{"id":"test-epic1","title":"Epic","type":"epic","status":"open","priority":1,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-epic1.1","title":"Child 1","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-epic1.2","title":"Child 2","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}
		if count != 3 {
			t.Errorf("Expected 3 issues imported, got %d", count)
		}

		// Now request the next child ID for the epic — this MUST be .3, not .1
		nextID, err := store.GetNextChildID(ctx, "test-epic1")
		if err != nil {
			t.Fatalf("GetNextChildID failed: %v", err)
		}
		if nextID != "test-epic1.3" {
			t.Errorf("Expected next child ID 'test-epic1.3', got %q (would overwrite existing child!)", nextID)
		}

		// Verify original children are still intact
		child1, err := store.GetIssue(ctx, "test-epic1.1")
		if err != nil {
			t.Fatalf("Failed to get child 1: %v", err)
		}
		if child1.Title != "Child 1" {
			t.Errorf("Child 1 title changed unexpectedly: got %q", child1.Title)
		}
		child2, err := store.GetIssue(ctx, "test-epic1.2")
		if err != nil {
			t.Fatalf("Failed to get child 2: %v", err)
		}
		if child2.Title != "Child 2" {
			t.Errorf("Child 2 title changed unexpectedly: got %q", child2.Title)
		}
	})

	t.Run("sets prefix from first issue when not configured", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStoreWithPrefix(t, dbPath, "") // Empty prefix

		jsonlContent := `{"id":"myprefix-abc123","title":"Test issue","type":"bug","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		// Clear any existing prefix
		_ = store.SetConfig(ctx, "issue_prefix", "")

		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 issue imported, got %d", count)
		}

		// Verify prefix was auto-detected
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get issue_prefix: %v", err)
		}
		if prefix != "myprefix" {
			t.Errorf("Expected auto-detected prefix 'myprefix', got %q", prefix)
		}
	})
}
