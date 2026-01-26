package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestReadJSONLToMap(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")

	// Create test JSONL with 3 issues
	issues := []types.Issue{
		{ID: "test-001", Title: "First", Status: types.StatusOpen},
		{ID: "test-002", Title: "Second", Status: types.StatusInProgress},
		{ID: "test-003", Title: "Third", Status: types.StatusClosed},
	}

	var content strings.Builder
	for _, issue := range issues {
		data, _ := json.Marshal(issue)
		content.Write(data)
		content.WriteString("\n")
	}

	if err := os.WriteFile(jsonlPath, []byte(content.String()), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Test readJSONLToMap
	issueMap, ids, err := readJSONLToMap(jsonlPath)
	if err != nil {
		t.Fatalf("readJSONLToMap failed: %v", err)
	}

	// Verify count
	if len(issueMap) != 3 {
		t.Errorf("expected 3 issues in map, got %d", len(issueMap))
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 IDs, got %d", len(ids))
	}

	// Verify order preserved
	expectedOrder := []string{"test-001", "test-002", "test-003"}
	for i, id := range ids {
		if id != expectedOrder[i] {
			t.Errorf("ID order mismatch at %d: expected %s, got %s", i, expectedOrder[i], id)
		}
	}

	// Verify content can be unmarshaled
	for id, rawJSON := range issueMap {
		var issue types.Issue
		if err := json.Unmarshal(rawJSON, &issue); err != nil {
			t.Errorf("failed to unmarshal issue %s: %v", id, err)
		}
		if issue.ID != id {
			t.Errorf("ID mismatch: expected %s, got %s", id, issue.ID)
		}
	}
}

func TestReadJSONLToMapWithMalformedLines(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")

	content := `{"id": "test-001", "title": "Good"}
{invalid json}
{"id": "test-002", "title": "Also Good"}

{"id": "", "title": "No ID"}
{"id": "test-003", "title": "Third Good"}
`

	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	issueMap, ids, err := readJSONLToMap(jsonlPath)
	if err != nil {
		t.Fatalf("readJSONLToMap failed: %v", err)
	}

	// Should have 3 valid issues (skipped invalid JSON, empty line, and empty ID)
	if len(issueMap) != 3 {
		t.Errorf("expected 3 issues, got %d", len(issueMap))
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 IDs, got %d", len(ids))
	}
}

func TestShouldUseIncrementalExport(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Set up global state
	oldStore := store
	oldDBPath := dbPath
	oldRootCtx := rootCtx
	store = s
	dbPath = testDB
	rootCtx = ctx
	defer func() {
		store = oldStore
		dbPath = oldDBPath
		rootCtx = oldRootCtx
	}()

	t.Run("no JSONL file returns false", func(t *testing.T) {
		useIncremental, _, err := shouldUseIncrementalExport(ctx, jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if useIncremental {
			t.Error("expected false when JSONL doesn't exist")
		}
	})

	t.Run("no dirty issues returns true with empty IDs", func(t *testing.T) {
		// Create JSONL file with some issues
		var content strings.Builder
		for i := 0; i < 10; i++ {
			issue := types.Issue{ID: "test-" + string(rune('a'+i)), Title: "Test"}
			data, _ := json.Marshal(issue)
			content.Write(data)
			content.WriteString("\n")
		}
		if err := os.WriteFile(jsonlPath, []byte(content.String()), 0644); err != nil {
			t.Fatalf("failed to write JSONL: %v", err)
		}

		// No dirty issues in database
		useIncremental, dirtyIDs, err := shouldUseIncrementalExport(ctx, jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// With 0 dirty issues, we return true to signal nothing needs export
		if !useIncremental {
			t.Error("expected true when no dirty issues (nothing to export)")
		}
		if len(dirtyIDs) != 0 {
			t.Errorf("expected 0 dirty IDs, got %d", len(dirtyIDs))
		}
	})

	t.Run("small repo with dirty issues returns false", func(t *testing.T) {
		// Create an issue to make it dirty
		issue := &types.Issue{Title: "Dirty Issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Small repo (10 issues in JSONL) below 1000 threshold
		useIncremental, _, err := shouldUseIncrementalExport(ctx, jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if useIncremental {
			t.Error("expected false for small repo below threshold")
		}
	})
}

func TestIncrementalExportIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Set up global state
	oldStore := store
	oldDBPath := dbPath
	oldRootCtx := rootCtx
	store = s
	dbPath = testDB
	rootCtx = ctx
	defer func() {
		store = oldStore
		dbPath = oldDBPath
		rootCtx = oldRootCtx
	}()

	// Create initial issues
	issues := []*types.Issue{
		{Title: "Issue 1", Description: "Desc 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Issue 2", Description: "Desc 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Issue 3", Description: "Desc 3", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Initial full export using exportToJSONL (not deferred, which calls ensureStoreActive)
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		t.Fatalf("initial export failed: %v", err)
	}

	// Verify initial export worked
	issueMap, _, err := readJSONLToMap(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read initial JSONL: %v", err)
	}
	if len(issueMap) != 3 {
		t.Errorf("expected 3 issues after initial export, got %d", len(issueMap))
	}

	// Update one issue
	if err := s.UpdateIssue(ctx, issues[0].ID, map[string]interface{}{"title": "Updated Issue 1"}, "test"); err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	// Verify dirty count
	dirtyIDs, err := s.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("failed to get dirty issues: %v", err)
	}
	if len(dirtyIDs) != 1 {
		t.Errorf("expected 1 dirty issue, got %d", len(dirtyIDs))
	}

	// Test incremental export (using performIncrementalExport directly)
	result, err := performIncrementalExport(ctx, jsonlPath, dirtyIDs)
	if err != nil {
		t.Fatalf("incremental export failed: %v", err)
	}

	// Verify result
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.ExportedIDs) != 1 {
		t.Errorf("expected 1 exported ID (dirty), got %d", len(result.ExportedIDs))
	}

	// Read back JSONL and verify
	issueMap, ids, err := readJSONLToMap(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}
	if len(issueMap) != 3 {
		t.Errorf("expected 3 issues in JSONL, got %d", len(issueMap))
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 IDs, got %d", len(ids))
	}

	// Verify updated issue content
	var updatedIssue types.Issue
	if err := json.Unmarshal(issueMap[issues[0].ID], &updatedIssue); err != nil {
		t.Fatalf("failed to unmarshal updated issue: %v", err)
	}
	if updatedIssue.Title != "Updated Issue 1" {
		t.Errorf("expected title 'Updated Issue 1', got '%s'", updatedIssue.Title)
	}
}

func TestIncrementalExportWithDeletion(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Set up global state
	oldStore := store
	oldDBPath := dbPath
	oldRootCtx := rootCtx
	store = s
	dbPath = testDB
	rootCtx = ctx
	defer func() {
		store = oldStore
		dbPath = oldDBPath
		rootCtx = oldRootCtx
	}()

	// Create initial issues
	issues := []*types.Issue{
		{Title: "Issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Initial export
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		t.Fatalf("initial export failed: %v", err)
	}

	// Get initial dirty count (should be 0 after export)
	dirtyIDs, _ := s.GetDirtyIssues(ctx)
	if len(dirtyIDs) != 0 {
		t.Errorf("expected 0 dirty issues after export, got %d", len(dirtyIDs))
	}

	// Delete one issue (soft delete creates tombstone)
	if err := s.DeleteIssue(ctx, issues[0].ID); err != nil {
		t.Fatalf("failed to delete issue: %v", err)
	}

	// Get dirty IDs after deletion
	dirtyIDs, err := s.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("failed to get dirty issues: %v", err)
	}

	// Perform incremental export
	if len(dirtyIDs) > 0 {
		_, err = performIncrementalExport(ctx, jsonlPath, dirtyIDs)
		if err != nil {
			t.Fatalf("incremental export failed: %v", err)
		}

		// Read back and verify tombstone is present
		issueMap, _, err := readJSONLToMap(jsonlPath)
		if err != nil {
			t.Fatalf("failed to read JSONL: %v", err)
		}

		// Should still have 2 entries (one is now tombstone)
		if len(issueMap) != 2 {
			t.Errorf("expected 2 issues in JSONL (including tombstone), got %d", len(issueMap))
		}

		// Check tombstone status
		var tombstone types.Issue
		if err := json.Unmarshal(issueMap[issues[0].ID], &tombstone); err != nil {
			t.Fatalf("failed to unmarshal tombstone: %v", err)
		}
		if tombstone.Status != types.StatusTombstone {
			t.Errorf("expected tombstone status, got %s", tombstone.Status)
		}
	}
}

func TestIncrementalExportWithNewIssue(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Set up global state
	oldStore := store
	oldDBPath := dbPath
	oldRootCtx := rootCtx
	store = s
	dbPath = testDB
	rootCtx = ctx
	defer func() {
		store = oldStore
		dbPath = oldDBPath
		rootCtx = oldRootCtx
	}()

	// Create initial issue
	issue1 := &types.Issue{Title: "Issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Initial export
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		t.Fatalf("initial export failed: %v", err)
	}

	// Create new issue
	issue2 := &types.Issue{Title: "Issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("failed to create issue 2: %v", err)
	}

	// Get dirty IDs
	dirtyIDs, err := s.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("failed to get dirty issues: %v", err)
	}
	if len(dirtyIDs) != 1 {
		t.Errorf("expected 1 dirty issue (new one), got %d", len(dirtyIDs))
	}

	// Perform incremental export
	_, err = performIncrementalExport(ctx, jsonlPath, dirtyIDs)
	if err != nil {
		t.Fatalf("incremental export failed: %v", err)
	}

	// Read back and verify new issue was added
	issueMap, ids, err := readJSONLToMap(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}

	if len(issueMap) != 2 {
		t.Errorf("expected 2 issues in JSONL, got %d", len(issueMap))
	}

	// Verify both issues exist
	if _, ok := issueMap[issue1.ID]; !ok {
		t.Errorf("issue 1 missing from JSONL")
	}
	if _, ok := issueMap[issue2.ID]; !ok {
		t.Errorf("issue 2 missing from JSONL")
	}

	// Verify sorted order
	for i := 0; i < len(ids)-1; i++ {
		if ids[i] > ids[i+1] {
			t.Errorf("IDs not sorted: %s > %s", ids[i], ids[i+1])
		}
	}
}
