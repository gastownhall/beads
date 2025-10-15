package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestExportImport(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "test.db")

	// Create test database with sample issues
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()

	// Create test issues
	now := time.Now()
	issues := []*types.Issue{
		{
			ID:          "test-1",
			Title:       "First issue",
			Description: "Description 1",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeBug,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "test-2",
			Title:       "Second issue",
			Description: "Description 2",
			Status:      types.StatusInProgress,
			Priority:    2,
			IssueType:   types.TypeFeature,
			Assignee:    "alice",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "test-3",
			Title:       "Third issue",
			Description: "Description 3",
			Status:      types.StatusClosed,
			Priority:    3,
			IssueType:   types.TypeTask,
			CreatedAt:   now,
			UpdatedAt:   now,
			ClosedAt:    &now,
		},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Test export
	t.Run("Export", func(t *testing.T) {
		exported, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		if len(exported) != 3 {
			t.Errorf("Expected 3 issues, got %d", len(exported))
		}

		// Verify issues are sorted by ID
		for i := 0; i < len(exported)-1; i++ {
			if exported[i].ID > exported[i+1].ID {
				t.Errorf("Issues not sorted by ID: %s > %s", exported[i].ID, exported[i+1].ID)
			}
		}
	})

	// Test JSONL format
	t.Run("JSONL Format", func(t *testing.T) {
		exported, _ := store.SearchIssues(ctx, "", types.IssueFilter{})

		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		for _, issue := range exported {
			if err := encoder.Encode(issue); err != nil {
				t.Fatalf("Failed to encode issue: %v", err)
			}
		}

		// Verify each line is valid JSON
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 3 {
			t.Errorf("Expected 3 JSONL lines, got %d", len(lines))
		}

		for i, line := range lines {
			var issue types.Issue
			if err := json.Unmarshal([]byte(line), &issue); err != nil {
				t.Errorf("Line %d is not valid JSON: %v", i, err)
			}
		}
	})

	// Test import into new database
	t.Run("Import", func(t *testing.T) {
		// Export from original database
		exported, _ := store.SearchIssues(ctx, "", types.IssueFilter{})

		// Create new database
		newDBPath := filepath.Join(tmpDir, "import-test.db")
		newStore, err := sqlite.New(newDBPath)
		if err != nil {
			t.Fatalf("Failed to create new storage: %v", err)
		}

		// Import issues
		for _, issue := range exported {
			if err := newStore.CreateIssue(ctx, issue, "import"); err != nil {
				t.Fatalf("Failed to import issue: %v", err)
			}
		}

		// Verify imported issues
		imported, err := newStore.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		if len(imported) != len(exported) {
			t.Errorf("Expected %d issues, got %d", len(exported), len(imported))
		}

		// Verify issue data
		for i := range imported {
			if imported[i].ID != exported[i].ID {
				t.Errorf("Issue %d: ID = %s, want %s", i, imported[i].ID, exported[i].ID)
			}
			if imported[i].Title != exported[i].Title {
				t.Errorf("Issue %d: Title = %s, want %s", i, imported[i].Title, exported[i].Title)
			}
		}
	})

	// Test update on import
	t.Run("Import Update", func(t *testing.T) {
		// Get first issue
		issue, err := store.GetIssue(ctx, "test-1")
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}

		// Modify it
		issue.Title = "Updated title"
		issue.Status = types.StatusClosed

		// Import as update
		updates := map[string]interface{}{
			"title":  issue.Title,
			"status": string(issue.Status),
		}
		if err := store.UpdateIssue(ctx, issue.ID, updates, "test"); err != nil {
			t.Fatalf("UpdateIssue failed: %v", err)
		}

		// Verify update
		updated, err := store.GetIssue(ctx, "test-1")
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}

		if updated.Title != "Updated title" {
			t.Errorf("Title = %s, want 'Updated title'", updated.Title)
		}
		if updated.Status != types.StatusClosed {
			t.Errorf("Status = %s, want %s", updated.Status, types.StatusClosed)
		}
	})

	// Test filtering on export
	t.Run("Export with Filter", func(t *testing.T) {
		status := types.StatusOpen
		filter := types.IssueFilter{
			Status: &status,
		}

		filtered, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}

		// Should only get open issues (test-1 might be updated, so check count > 0)
		for _, issue := range filtered {
			if issue.Status != types.StatusOpen {
				t.Errorf("Expected only open issues, got %s", issue.Status)
			}
		}
	})
}

func TestExportEmpty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "empty.db")
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()

	// Export from empty database
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(issues) != 0 {
		t.Errorf("Expected 0 issues, got %d", len(issues))
	}
}

func TestImportInvalidJSON(t *testing.T) {
	invalidJSON := []string{
		`{"id":"test-1"`,                    // Incomplete JSON
		`{"id":"test-1","title":}`,          // Invalid syntax
		`not json at all`,                   // Not JSON
		`{"id":"","title":"No ID"}`,         // Empty ID
	}

	for i, line := range invalidJSON {
		var issue types.Issue
		err := json.Unmarshal([]byte(line), &issue)
		if err == nil && line != invalidJSON[3] { // Empty ID case will unmarshal but fail validation
			t.Errorf("Case %d: Expected unmarshal error for invalid JSON: %s", i, line)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	// Create original database
	tmpDir, err := os.MkdirTemp("", "bd-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath := filepath.Join(tmpDir, "original.db")
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()

	// Create issue with all fields populated
	estimatedMinutes := 120
	closedAt := time.Now()
	original := &types.Issue{
		ID:                 "test-1",
		Title:              "Full issue",
		Description:        "Description",
		Design:             "Design doc",
		AcceptanceCriteria: "Criteria",
		Notes:              "Notes",
		Status:             types.StatusClosed,
		Priority:           1,
		IssueType:          types.TypeFeature,
		Assignee:           "alice",
		EstimatedMinutes:   &estimatedMinutes,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
		ClosedAt:           &closedAt,
	}

	if err := store.CreateIssue(ctx, original, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	if err := encoder.Encode(original); err != nil {
		t.Fatalf("Failed to encode: %v", err)
	}

	// Import from JSONL
	var decoded types.Issue
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	// Verify all fields preserved
	if decoded.ID != original.ID {
		t.Errorf("ID = %s, want %s", decoded.ID, original.ID)
	}
	if decoded.Title != original.Title {
		t.Errorf("Title = %s, want %s", decoded.Title, original.Title)
	}
	if decoded.Description != original.Description {
		t.Errorf("Description = %s, want %s", decoded.Description, original.Description)
	}
	if decoded.EstimatedMinutes == nil || *decoded.EstimatedMinutes != *original.EstimatedMinutes {
		t.Errorf("EstimatedMinutes = %v, want %v", decoded.EstimatedMinutes, original.EstimatedMinutes)
	}
}
