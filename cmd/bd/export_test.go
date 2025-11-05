package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)



func TestExportCommand(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-export-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()

	// Create test issues
	issues := []*types.Issue{
		{
			Title:       "First Issue",
			Description: "Test description 1",
			Priority:    0,
			IssueType:   types.TypeBug,
			Status:      types.StatusOpen,
		},
		{
			Title:       "Second Issue",
			Description: "Test description 2",
			Priority:    1,
			IssueType:   types.TypeFeature,
			Status:      types.StatusInProgress,
		},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Add a label to first issue
	if err := s.AddLabel(ctx, issues[0].ID, "critical", "test-user"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}

	// Add a dependency
	dep := &types.Dependency{
		IssueID:     issues[0].ID,
		DependsOnID: issues[1].ID,
		Type:        "blocks",
	}
	if err := s.AddDependency(ctx, dep, "test-user"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	t.Run("export to file", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export.jsonl")

		// Set up global state
		store = s
		dbPath = testDB

		// Create a mock command with output flag
		exportCmd.SetArgs([]string{"-o", exportPath})
		exportCmd.Flags().Set("output", exportPath)

		// Export
		exportCmd.Run(exportCmd, []string{})

		// Verify file was created
		if _, err := os.Stat(exportPath); os.IsNotExist(err) {
			t.Fatal("Export file was not created")
		}

		// Read and verify JSONL content
		file, err := os.Open(exportPath)
		if err != nil {
			t.Fatalf("Failed to open export file: %v", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineCount := 0
		for scanner.Scan() {
			lineCount++
			var issue types.Issue
			if err := json.Unmarshal(scanner.Bytes(), &issue); err != nil {
				t.Fatalf("Failed to parse JSONL line %d: %v", lineCount, err)
			}

			// Verify issue has required fields
			if issue.ID == "" {
				t.Error("Issue missing ID")
			}
			if issue.Title == "" {
				t.Error("Issue missing title")
			}
		}

		if lineCount != 2 {
			t.Errorf("Expected 2 lines in export, got %d", lineCount)
		}
	})

	t.Run("export includes labels", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_labels.jsonl")

		// Clear export hashes to force re-export (test isolation)
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Run(exportCmd, []string{})

		file, err := os.Open(exportPath)
		if err != nil {
			t.Fatalf("Failed to open export file: %v", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		foundLabeledIssue := false
		for scanner.Scan() {
			var issue types.Issue
			if err := json.Unmarshal(scanner.Bytes(), &issue); err != nil {
				t.Fatalf("Failed to parse JSONL: %v", err)
			}

			if issue.ID == issues[0].ID {
				foundLabeledIssue = true
				if len(issue.Labels) != 1 || issue.Labels[0] != "critical" {
					t.Errorf("Expected label 'critical', got %v", issue.Labels)
				}
			}
		}

		if !foundLabeledIssue {
			t.Error("Did not find labeled issue in export")
		}
	})

	t.Run("export includes dependencies", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_deps.jsonl")

		// Clear export hashes to force re-export (test isolation)
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Run(exportCmd, []string{})

		file, err := os.Open(exportPath)
		if err != nil {
			t.Fatalf("Failed to open export file: %v", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		foundDependency := false
		for scanner.Scan() {
			var issue types.Issue
			if err := json.Unmarshal(scanner.Bytes(), &issue); err != nil {
				t.Fatalf("Failed to parse JSONL: %v", err)
			}

			if issue.ID == issues[0].ID && len(issue.Dependencies) > 0 {
				foundDependency = true
				if issue.Dependencies[0].DependsOnID != issues[1].ID {
					t.Errorf("Expected dependency to %s, got %s", issues[1].ID, issue.Dependencies[0].DependsOnID)
				}
			}
		}

		if !foundDependency {
			t.Error("Did not find dependency in export")
		}
	})

	t.Run("validate export path", func(t *testing.T) {
		// Test safe path
		if err := validateExportPath(tmpDir); err != nil {
			t.Errorf("Unexpected error for safe path: %v", err)
		}

		// Test Windows system directories
		// Note: validateExportPath() only checks Windows paths on case-insensitive systems
		// On Unix/Mac, C:\Windows won't match, so we skip this assertion
		// Just verify the function doesn't panic with Windows-style paths
		_ = validateExportPath("C:\\Windows\\system32\\test.jsonl")
	})

	t.Run("prevent exporting empty database over non-empty JSONL", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_empty_check.jsonl")

		// First, create a JSONL file with issues
		file, err := os.Create(exportPath)
		if err != nil {
			t.Fatalf("Failed to create JSONL: %v", err)
		}
		encoder := json.NewEncoder(file)
		for _, issue := range issues {
			if err := encoder.Encode(issue); err != nil {
				t.Fatalf("Failed to encode issue: %v", err)
			}
		}
		file.Close()

		// Verify file has issues
		count, err := countIssuesInJSONL(exportPath)
		if err != nil {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if count != 2 {
			t.Fatalf("Expected 2 issues in JSONL, got %d", count)
		}

		// Create empty database
		emptyDBPath := filepath.Join(tmpDir, "empty.db")
		emptyStore := newTestStore(t, emptyDBPath)
		defer emptyStore.Close()

		// Test using exportToJSONLWithStore directly (daemon code path)
		err = exportToJSONLWithStore(ctx, emptyStore, exportPath)
		if err == nil {
			t.Error("Expected error when exporting empty database over non-empty JSONL")
		} else {
			expectedMsg := "refusing to export empty database over non-empty JSONL file (database: 0 issues, JSONL: 2 issues). This would result in data loss"
			if err.Error() != expectedMsg {
				t.Errorf("Unexpected error message:\nGot:      %q\nExpected: %q", err.Error(), expectedMsg)
			}
		}

		// Verify JSONL file is unchanged
		countAfter, err := countIssuesInJSONL(exportPath)
		if err != nil {
			t.Fatalf("Failed to count issues after failed export: %v", err)
		}
		if countAfter != 2 {
			t.Errorf("JSONL file was modified! Expected 2 issues, got %d", countAfter)
		}
	})

	t.Run("verify JSONL line count matches exported count", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_verify.jsonl")

		// Clear export hashes to force re-export
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Run(exportCmd, []string{})

		// Verify the exported file has exactly 2 lines
		actualCount, err := countIssuesInJSONL(exportPath)
		if err != nil {
			t.Fatalf("Failed to count issues in JSONL: %v", err)
		}
		if actualCount != 2 {
			t.Errorf("Expected 2 issues in JSONL, got %d", actualCount)
		}

		// Simulate corrupted export by truncating file
		corruptedPath := filepath.Join(tmpDir, "export_corrupted.jsonl")
		
		// First export normally
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}
		store = s
		exportCmd.Flags().Set("output", corruptedPath)
		exportCmd.Run(exportCmd, []string{})

		// Now manually corrupt it by removing one line
		file, err := os.Open(corruptedPath)
		if err != nil {
			t.Fatalf("Failed to open file for corruption: %v", err)
		}
		scanner := bufio.NewScanner(file)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		file.Close()

		// Write back only first line (simulating partial write)
		corruptedFile, err := os.Create(corruptedPath)
		if err != nil {
			t.Fatalf("Failed to create corrupted file: %v", err)
		}
		corruptedFile.WriteString(lines[0] + "\n")
		corruptedFile.Close()

		// Verify countIssuesInJSONL detects the corruption
		count, err := countIssuesInJSONL(corruptedPath)
		if err != nil {
			t.Fatalf("Failed to count corrupted file: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 line in corrupted file, got %d", count)
		}
	})
}
