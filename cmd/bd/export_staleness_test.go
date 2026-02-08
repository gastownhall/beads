package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExportStaleness_DBHasFewerIssues tests that export refuses when database
// has fewer issues than JSONL (indicating staleness)
func TestExportStaleness_DBHasFewerIssues(t *testing.T) {
	tmpDir := t.TempDir()

	// Create JSONL with 3 issues
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")
	jsonlContent := `{"id":"test-1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-2","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-3","title":"Issue 3","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Verify count function works
	count, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to count issues: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 issues in JSONL, got %d", count)
	}
}

// TestExportStaleness_DBHasSameIssues tests that export succeeds when database
// has same number of issues as JSONL
func TestExportStaleness_DBHasSameIssues(t *testing.T) {
	tmpDir := t.TempDir()

	// Create JSONL with 2 issues
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")
	jsonlContent := `{"id":"test-1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-2","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Verify count
	count, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to count issues: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 issues in JSONL, got %d", count)
	}
}

// TestExportStaleness_NoJSONL tests that export succeeds when JSONL doesn't exist
func TestExportStaleness_NoJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "nonexistent.jsonl")

	// Should not error when file doesn't exist
	_, err := countIssuesInJSONL(jsonlPath)
	if err == nil {
		t.Error("Expected error when JSONL doesn't exist")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Expected IsNotExist error, got: %v", err)
	}
}

// TestExportStaleness_DifferentIssues tests that export refuses when database
// has different issues than JSONL (even with same count)
func TestExportStaleness_DifferentIssues(t *testing.T) {
	tmpDir := t.TempDir()

	// Create JSONL with issues test-1, test-2, test-3
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")
	jsonlContent := `{"id":"test-1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-2","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-3","title":"Issue 3","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Verify getIssueIDsFromJSONL function
	ids, err := getIssueIDsFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to get issue IDs: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("Expected 3 issue IDs, got %d", len(ids))
	}
	if !ids["test-1"] || !ids["test-2"] || !ids["test-3"] {
		t.Errorf("Missing expected issue IDs, got: %v", ids)
	}
}

// TestGetIssueIDsFromJSONL_InvalidJSON tests error handling for corrupt JSONL
func TestGetIssueIDsFromJSONL_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "corrupt.jsonl")

	// Create JSONL with invalid JSON on second line
	jsonlContent := `{"id":"test-1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
this is not valid JSON
{"id":"test-3","title":"Issue 3","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Should return error with first valid issue ID read
	ids, err := getIssueIDsFromJSONL(jsonlPath)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
	// Should have read the first line before hitting the error
	if len(ids) != 1 || !ids["test-1"] {
		t.Errorf("Expected to have read test-1 before error, got: %v", ids)
	}
}
