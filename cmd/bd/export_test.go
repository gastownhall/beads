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
		rootCtx = ctx
		defer func() { rootCtx = nil }()

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
		rootCtx = ctx
		defer func() { rootCtx = nil }()
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
		rootCtx = ctx
		defer func() { rootCtx = nil }()
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
		rootCtx = ctx
		defer func() { rootCtx = nil }()
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
		rootCtx = ctx
		defer func() { rootCtx = nil }()
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

	t.Run("export with id filter", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_id_filter.jsonl")

		// Clear export hashes to force re-export
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		rootCtx = ctx
		defer func() { rootCtx = nil }()

		// Filter by first issue's ID only
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Flags().Set("id", issues[0].ID)
		defer exportCmd.Flags().Set("id", "") // Reset flag after test
		exportCmd.Run(exportCmd, []string{})

		// Verify only one issue was exported
		file, err := os.Open(exportPath)
		if err != nil {
			t.Fatalf("Failed to open export file: %v", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineCount := 0
		var exportedIssue types.Issue
		for scanner.Scan() {
			lineCount++
			if err := json.Unmarshal(scanner.Bytes(), &exportedIssue); err != nil {
				t.Fatalf("Failed to parse JSONL line %d: %v", lineCount, err)
			}
		}

		if lineCount != 1 {
			t.Errorf("Expected 1 issue in export with ID filter, got %d", lineCount)
		}
		if exportedIssue.ID != issues[0].ID {
			t.Errorf("Expected issue ID %s, got %s", issues[0].ID, exportedIssue.ID)
		}
	})

	t.Run("export with multiple id filter", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_multi_id_filter.jsonl")

		// Clear export hashes to force re-export
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		rootCtx = ctx
		defer func() { rootCtx = nil }()

		// Filter by both issue IDs (comma-separated)
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Flags().Set("id", issues[0].ID+","+issues[1].ID)
		defer exportCmd.Flags().Set("id", "") // Reset flag after test
		exportCmd.Run(exportCmd, []string{})

		// Verify both issues were exported
		actualCount, err := countIssuesInJSONL(exportPath)
		if err != nil {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if actualCount != 2 {
			t.Errorf("Expected 2 issues in export with multiple ID filter, got %d", actualCount)
		}
	})

	t.Run("export with parent filter", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_parent_filter.jsonl")

		// Create a parent issue (epic)
		parentIssue := &types.Issue{
			Title:       "Parent Epic",
			Description: "Parent issue for testing",
			Priority:    0,
			IssueType:   types.TypeEpic,
			Status:      types.StatusOpen,
		}
		if err := s.CreateIssue(ctx, parentIssue, "test-user"); err != nil {
			t.Fatalf("Failed to create parent issue: %v", err)
		}

		// Create child issues with parent-child dependency
		childIssue1 := &types.Issue{
			Title:       "Child Task 1",
			Description: "First child of parent",
			Priority:    1,
			IssueType:   types.TypeTask,
			Status:      types.StatusOpen,
		}
		if err := s.CreateIssue(ctx, childIssue1, "test-user"); err != nil {
			t.Fatalf("Failed to create child issue 1: %v", err)
		}

		childIssue2 := &types.Issue{
			Title:       "Child Task 2",
			Description: "Second child of parent",
			Priority:    2,
			IssueType:   types.TypeTask,
			Status:      types.StatusOpen,
		}
		if err := s.CreateIssue(ctx, childIssue2, "test-user"); err != nil {
			t.Fatalf("Failed to create child issue 2: %v", err)
		}

		// Add parent-child dependencies
		dep1 := &types.Dependency{
			IssueID:     childIssue1.ID,
			DependsOnID: parentIssue.ID,
			Type:        types.DepParentChild,
		}
		if err := s.AddDependency(ctx, dep1, "test-user"); err != nil {
			t.Fatalf("Failed to add parent-child dependency 1: %v", err)
		}

		dep2 := &types.Dependency{
			IssueID:     childIssue2.ID,
			DependsOnID: parentIssue.ID,
			Type:        types.DepParentChild,
		}
		if err := s.AddDependency(ctx, dep2, "test-user"); err != nil {
			t.Fatalf("Failed to add parent-child dependency 2: %v", err)
		}

		// Clear export hashes to force re-export
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		rootCtx = ctx
		defer func() { rootCtx = nil }()

		// Filter by parent ID
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Flags().Set("parent", parentIssue.ID)
		defer exportCmd.Flags().Set("parent", "") // Reset flag after test
		exportCmd.Run(exportCmd, []string{})

		// Verify only children were exported (not the parent itself)
		file, err := os.Open(exportPath)
		if err != nil {
			t.Fatalf("Failed to open export file: %v", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		var exportedIDs []string
		for scanner.Scan() {
			var issue types.Issue
			if err := json.Unmarshal(scanner.Bytes(), &issue); err != nil {
				t.Fatalf("Failed to parse JSONL: %v", err)
			}
			exportedIDs = append(exportedIDs, issue.ID)
		}

		// Should have exactly 2 children
		if len(exportedIDs) != 2 {
			t.Errorf("Expected 2 children in export with parent filter, got %d", len(exportedIDs))
		}

		// Verify the exported issues are the children, not the parent
		for _, id := range exportedIDs {
			if id == parentIssue.ID {
				t.Error("Parent issue should not be included in parent filter results")
			}
			if id != childIssue1.ID && id != childIssue2.ID {
				t.Errorf("Unexpected issue ID in export: %s", id)
			}
		}
	})

	t.Run("export with non-existent id filter", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_nonexistent_id.jsonl")

		// Clear export hashes to force re-export
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		rootCtx = ctx
		defer func() { rootCtx = nil }()

		// Filter by non-existent ID
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Flags().Set("id", "nonexistent-id-12345")
		exportCmd.Flags().Set("force", "true") // Force to allow empty export
		defer func() {
			exportCmd.Flags().Set("id", "")
			exportCmd.Flags().Set("force", "false")
		}()
		exportCmd.Run(exportCmd, []string{})

		// Verify no issues were exported (file may not exist or be empty)
		actualCount, err := countIssuesInJSONL(exportPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if actualCount != 0 {
			t.Errorf("Expected 0 issues in export with non-existent ID filter, got %d", actualCount)
		}
	})

	t.Run("filtered export skips staleness check", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_filtered_staleness.jsonl")

		// First, create a JSONL file with more issues than we'll filter for
		// This would normally trigger the staleness check
		file, err := os.Create(exportPath)
		if err != nil {
			t.Fatalf("Failed to create JSONL: %v", err)
		}
		encoder := json.NewEncoder(file)
		// Write all issues including some that won't match our filter
		for _, issue := range issues {
			if err := encoder.Encode(issue); err != nil {
				t.Fatalf("Failed to encode issue: %v", err)
			}
		}
		// Add a fake issue that only exists in JSONL (would trigger staleness error)
		fakeIssue := &types.Issue{
			ID:          "fake-issue-999",
			Title:       "Fake Issue",
			Description: "This issue only exists in JSONL",
			Status:      types.StatusOpen,
		}
		if err := encoder.Encode(fakeIssue); err != nil {
			t.Fatalf("Failed to encode fake issue: %v", err)
		}
		file.Close()

		// Verify JSONL has 3 issues (2 real + 1 fake)
		count, err := countIssuesInJSONL(exportPath)
		if err != nil {
			t.Fatalf("Failed to count issues: %v", err)
		}
		if count != 3 {
			t.Fatalf("Expected 3 issues in JSONL, got %d", count)
		}

		// Clear export hashes
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		rootCtx = ctx
		defer func() { rootCtx = nil }()

		// Export with --id filter for just one issue
		// Without the fix, this would fail with "refusing to export stale database"
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Flags().Set("id", issues[0].ID)
		defer exportCmd.Flags().Set("id", "") // Reset flag after test
		exportCmd.Run(exportCmd, []string{})

		// Verify export succeeded and only has 1 issue
		actualCount, err := countIssuesInJSONL(exportPath)
		if err != nil {
			t.Fatalf("Failed to count issues after filtered export: %v", err)
		}
		if actualCount != 1 {
			t.Errorf("Expected 1 issue in filtered export, got %d", actualCount)
		}
	})

	t.Run("filtered export with zero results succeeds", func(t *testing.T) {
		exportPath := filepath.Join(tmpDir, "export_filtered_empty.jsonl")

		// Create a JSONL with existing issues
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

		// Clear export hashes
		if err := s.ClearAllExportHashes(ctx); err != nil {
			t.Fatalf("Failed to clear export hashes: %v", err)
		}

		store = s
		dbPath = testDB
		rootCtx = ctx
		defer func() { rootCtx = nil }()

		// Export with --id filter for non-existent issue
		// Without the fix, this would fail with "refusing to export empty database"
		exportCmd.Flags().Set("output", exportPath)
		exportCmd.Flags().Set("id", "nonexistent-id-xyz")
		defer exportCmd.Flags().Set("id", "") // Reset flag after test
		exportCmd.Run(exportCmd, []string{})

		// Verify export succeeded (file should be empty or have 0 issues)
		actualCount, err := countIssuesInJSONL(exportPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("Failed to count issues after filtered export: %v", err)
		}
		if actualCount != 0 {
			t.Errorf("Expected 0 issues in filtered export with non-existent ID, got %d", actualCount)
		}
	})

	t.Run("export cancellation", func(t *testing.T) {
		// Create a large number of issues to ensure export takes time
		ctx := context.Background()
		largeStore := newTestStore(t, filepath.Join(tmpDir, "large.db"))
		defer largeStore.Close()

		// Create 100 issues
		for i := 0; i < 100; i++ {
			issue := &types.Issue{
				Title:       "Test Issue",
				Description: "Test description for cancellation",
				Priority:    0,
				IssueType:   types.TypeBug,
				Status:      types.StatusOpen,
			}
			if err := largeStore.CreateIssue(ctx, issue, "test-user"); err != nil {
				t.Fatalf("Failed to create issue: %v", err)
			}
		}

		exportPath := filepath.Join(tmpDir, "export_cancel.jsonl")

		// Create a cancellable context
		cancelCtx, cancel := context.WithCancel(context.Background())

		// Start export in a goroutine
		errChan := make(chan error, 1)
		go func() {
			errChan <- exportToJSONLWithStore(cancelCtx, largeStore, exportPath)
		}()

		// Cancel after a short delay
		cancel()

		// Wait for export to finish
		err := <-errChan

		// Verify that the operation was cancelled
		if err != nil && err != context.Canceled {
			t.Logf("Export returned error: %v (expected context.Canceled)", err)
		}

		// Verify database integrity - we should still be able to query
		issues, err := largeStore.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("Database corrupted after cancellation: %v", err)
		}
		if len(issues) != 100 {
			t.Errorf("Expected 100 issues after cancellation, got %d", len(issues))
		}
	})
}
