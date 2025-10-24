package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadIssueIDsFromFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-delete-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("read valid IDs from file", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "ids.txt")
		content := "bd-1\nbd-2\nbd-3\n"
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		ids, err := readIssueIDsFromFile(testFile)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(ids) != 3 {
			t.Errorf("Expected 3 IDs, got %d", len(ids))
		}

		expected := []string{"bd-1", "bd-2", "bd-3"}
		for i, id := range ids {
			if id != expected[i] {
				t.Errorf("Expected ID %s at position %d, got %s", expected[i], i, id)
			}
		}
	})

	t.Run("skip empty lines and comments", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "ids_with_comments.txt")
		content := "bd-1\n\n# This is a comment\nbd-2\n  \nbd-3\n"
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		ids, err := readIssueIDsFromFile(testFile)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(ids) != 3 {
			t.Errorf("Expected 3 IDs (skipping comments/empty), got %d", len(ids))
		}
	})

	t.Run("handle non-existent file", func(t *testing.T) {
		_, err := readIssueIDsFromFile(filepath.Join(tmpDir, "nonexistent.txt"))
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
	})
}

func TestUniqueStrings(t *testing.T) {
	t.Run("remove duplicates", func(t *testing.T) {
		input := []string{"a", "b", "a", "c", "b", "d"}
		result := uniqueStrings(input)

		if len(result) != 4 {
			t.Errorf("Expected 4 unique strings, got %d", len(result))
		}

		// Verify all unique values are present
		seen := make(map[string]bool)
		for _, s := range result {
			if seen[s] {
				t.Errorf("Duplicate found in result: %s", s)
			}
			seen[s] = true
		}
	})

	t.Run("handle empty input", func(t *testing.T) {
		result := uniqueStrings([]string{})
		if len(result) != 0 {
			t.Errorf("Expected empty result, got %d items", len(result))
		}
	})

	t.Run("handle all unique", func(t *testing.T) {
		input := []string{"a", "b", "c"}
		result := uniqueStrings(input)

		if len(result) != 3 {
			t.Errorf("Expected 3 items, got %d", len(result))
		}
	})
}
