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

// exportImportHelper provides test setup and assertion methods
type exportImportHelper struct {
	t     *testing.T
	ctx   context.Context
	store *sqlite.SQLiteStorage
}

func newExportImportHelper(t *testing.T, store *sqlite.SQLiteStorage) *exportImportHelper {
	return &exportImportHelper{t: t, ctx: context.Background(), store: store}
}

func (h *exportImportHelper) createIssue(id, title, desc string, status types.Status, priority int, issueType types.IssueType, assignee string, closedAt *time.Time) *types.Issue {
	now := time.Now()
	issue := &types.Issue{
		ID:          id,
		Title:       title,
		Description: desc,
		Status:      status,
		Priority:    priority,
		IssueType:   issueType,
		Assignee:    assignee,
		CreatedAt:   now,
		UpdatedAt:   now,
		ClosedAt:    closedAt,
	}
	if err := h.store.CreateIssue(h.ctx, issue, "test"); err != nil {
		h.t.Fatalf("Failed to create issue: %v", err)
	}
	return issue
}

func (h *exportImportHelper) createFullIssue(id string, estimatedMinutes int) *types.Issue {
	closedAt := time.Now()
	issue := &types.Issue{
		ID:                 id,
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
	if err := h.store.CreateIssue(h.ctx, issue, "test"); err != nil {
		h.t.Fatalf("Failed to create issue: %v", err)
	}
	return issue
}

func (h *exportImportHelper) searchIssues(filter types.IssueFilter) []*types.Issue {
	issues, err := h.store.SearchIssues(h.ctx, "", filter)
	if err != nil {
		h.t.Fatalf("SearchIssues failed: %v", err)
	}
	return issues
}

func (h *exportImportHelper) getIssue(id string) *types.Issue {
	issue, err := h.store.GetIssue(h.ctx, id)
	if err != nil {
		h.t.Fatalf("GetIssue failed: %v", err)
	}
	return issue
}

func (h *exportImportHelper) updateIssue(id string, updates map[string]interface{}) {
	if err := h.store.UpdateIssue(h.ctx, id, updates, "test"); err != nil {
		h.t.Fatalf("UpdateIssue failed: %v", err)
	}
}

func (h *exportImportHelper) assertCount(count, expected int, item string) {
	if count != expected {
		h.t.Errorf("Expected %d %s, got %d", expected, item, count)
	}
}

func (h *exportImportHelper) assertEqual(expected, actual interface{}, field string) {
	if expected != actual {
		h.t.Errorf("%s = %v, want %v", field, actual, expected)
	}
}

func (h *exportImportHelper) assertSorted(issues []*types.Issue) {
	for i := 0; i < len(issues)-1; i++ {
		if issues[i].ID > issues[i+1].ID {
			h.t.Errorf("Issues not sorted by ID: %s > %s", issues[i].ID, issues[i+1].ID)
		}
	}
}

func (h *exportImportHelper) encodeJSONL(issues []*types.Issue) *bytes.Buffer {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			h.t.Fatalf("Failed to encode issue: %v", err)
		}
	}
	return &buf
}

func (h *exportImportHelper) validateJSONLines(buf *bytes.Buffer, expectedCount int) {
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	h.assertCount(len(lines), expectedCount, "JSONL lines")
	for i, line := range lines {
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			h.t.Errorf("Line %d is not valid JSON: %v", i, err)
		}
	}
}

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
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	h := newExportImportHelper(t, store)
	now := time.Now()

	// Create test issues
	h.createIssue("test-1", "First issue", "Description 1", types.StatusOpen, 1, types.TypeBug, "", nil)
	h.createIssue("test-2", "Second issue", "Description 2", types.StatusInProgress, 2, types.TypeFeature, "alice", nil)
	h.createIssue("test-3", "Third issue", "Description 3", types.StatusClosed, 3, types.TypeTask, "", &now)

	// Test export
	t.Run("Export", func(t *testing.T) {
		exported := h.searchIssues(types.IssueFilter{})
		h.assertCount(len(exported), 3, "issues")
		h.assertSorted(exported)
	})

	// Test JSONL format
	t.Run("JSONL Format", func(t *testing.T) {
		exported := h.searchIssues(types.IssueFilter{})
		buf := h.encodeJSONL(exported)
		h.validateJSONLines(buf, 3)
	})

	// Test import into new database
	t.Run("Import", func(t *testing.T) {
		exported := h.searchIssues(types.IssueFilter{})
		newDBPath := filepath.Join(tmpDir, "import-test.db")
		newStore, err := sqlite.New(newDBPath)
		if err != nil {
			t.Fatalf("Failed to create new storage: %v", err)
		}
		newHelper := newExportImportHelper(t, newStore)
		for _, issue := range exported {
			newHelper.createIssue(issue.ID, issue.Title, issue.Description, issue.Status, issue.Priority, issue.IssueType, issue.Assignee, issue.ClosedAt)
		}
		imported := newHelper.searchIssues(types.IssueFilter{})
		newHelper.assertCount(len(imported), len(exported), "issues")
		for i := range imported {
			newHelper.assertEqual(exported[i].ID, imported[i].ID, "ID")
			newHelper.assertEqual(exported[i].Title, imported[i].Title, "Title")
		}
	})

	// Test update on import
	t.Run("Import Update", func(t *testing.T) {
		issue := h.getIssue("test-1")
		updates := map[string]interface{}{"title": "Updated title", "status": string(types.StatusClosed)}
		h.updateIssue(issue.ID, updates)
		updated := h.getIssue("test-1")
		h.assertEqual("Updated title", updated.Title, "Title")
		h.assertEqual(types.StatusClosed, updated.Status, "Status")
	})

	// Test filtering on export
	t.Run("Export with Filter", func(t *testing.T) {
		status := types.StatusOpen
		filtered := h.searchIssues(types.IssueFilter{Status: &status})
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
		`{"id":"test-1"`,            // Incomplete JSON
		`{"id":"test-1","title":}`,  // Invalid syntax
		`not json at all`,           // Not JSON
		`{"id":"","title":"No ID"}`, // Empty ID
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

	h := newExportImportHelper(t, store)
	original := h.createFullIssue("test-1", 120)

	// Export to JSONL
	buf := h.encodeJSONL([]*types.Issue{original})

	// Import from JSONL
	var decoded types.Issue
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	// Verify all fields preserved
	h.assertEqual(original.ID, decoded.ID, "ID")
	h.assertEqual(original.Title, decoded.Title, "Title")
	h.assertEqual(original.Description, decoded.Description, "Description")
	if decoded.EstimatedMinutes == nil || *decoded.EstimatedMinutes != *original.EstimatedMinutes {
		t.Errorf("EstimatedMinutes = %v, want %v", decoded.EstimatedMinutes, original.EstimatedMinutes)
	}
}
