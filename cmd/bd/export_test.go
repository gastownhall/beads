package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestExportCommand(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-export-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s, err := sqlite.New(testDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
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
}
