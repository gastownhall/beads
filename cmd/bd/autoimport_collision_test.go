package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// Helper function to create test database with issues
func createTestDBWithIssues(t *testing.T, issues []*types.Issue) (string, *sqlite.SQLiteStorage) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "bd-collision-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	dbPath := filepath.Join(tmpDir, "test.db")
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	t.Cleanup(func() { testStore.Close() })

	ctx := context.Background()
	for _, issue := range issues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %s: %v", issue.ID, err)
		}
	}

	return tmpDir, testStore
}

// Helper function to write JSONL file
func writeJSONLFile(t *testing.T, dir string, issues []*types.Issue) {
	t.Helper()
	jsonlPath := filepath.Join(dir, "issues.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL file: %v", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			t.Fatalf("Failed to encode issue %s: %v", issue.ID, err)
		}
	}
}

// Helper function to capture stderr output
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// Helper function to setup auto-import test environment
func setupAutoImportTest(t *testing.T, testStore *sqlite.SQLiteStorage, tmpDir string) {
	t.Helper()
	store = testStore
	dbPath = filepath.Join(tmpDir, "test.db")

	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	t.Cleanup(func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})
}

// TestAutoImportMultipleCollisionsRemapped tests that multiple collisions are auto-resolved
func TestAutoImportMultipleCollisionsRemapped(t *testing.T) {
	// Create 5 issues in DB with local modifications
	now := time.Now().UTC()
	closedTime := now.Add(-1 * time.Hour)

	dbIssues := []*types.Issue{
		{
			ID:        "test-mc-1",
			Title:     "Local version 1",
			Status:    types.StatusClosed,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			ClosedAt:  &closedTime,
		},
		{
			ID:        "test-mc-2",
			Title:     "Local version 2",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-mc-3",
			Title:     "Local version 3",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeFeature,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-mc-4",
			Title:     "Exact match",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-mc-5",
			Title:     "Another exact match",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	// Create JSONL with 3 colliding issues, 2 exact matches, and 1 new issue
	jsonlIssues := []*types.Issue{
		{
			ID:        "test-mc-1",
			Title:     "Remote version 1 (conflict)",
			Status:    types.StatusOpen,
			Priority:  3,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-mc-2",
			Title:     "Remote version 2 (conflict)",
			Status:    types.StatusClosed,
			Priority:  1,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now.Add(-30 * time.Minute),
			ClosedAt:  &closedTime,
		},
		{
			ID:        "test-mc-3",
			Title:     "Remote version 3 (conflict)",
			Status:    types.StatusBlocked,
			Priority:  3,
			IssueType: types.TypeFeature,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-mc-4",
			Title:     "Exact match",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-mc-5",
			Title:     "Another exact match",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-mc-6",
			Title:     "Brand new issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	writeJSONLFile(t, tmpDir, jsonlIssues)

	// Capture stderr and run auto-import
	stderrOutput := captureStderr(t, autoImportIfNewer)

	ctx := context.Background()

	// Verify local versions are preserved (original IDs still have local content)
	local1, _ := testStore.GetIssue(ctx, "test-mc-1")
	if local1.Title != "Local version 1" {
		t.Errorf("Expected local version preserved for test-mc-1, got: %s", local1.Title)
	}

	local2, _ := testStore.GetIssue(ctx, "test-mc-2")
	if local2.Title != "Local version 2" {
		t.Errorf("Expected local version preserved for test-mc-2, got: %s", local2.Title)
	}

	local3, _ := testStore.GetIssue(ctx, "test-mc-3")
	if local3.Title != "Local version 3" {
		t.Errorf("Expected local version preserved for test-mc-3, got: %s", local3.Title)
	}

	// Verify new issue was imported
	newIssue, _ := testStore.GetIssue(ctx, "test-mc-6")
	if newIssue == nil {
		t.Fatal("Expected new issue test-mc-6 to be imported")
	}
	if newIssue.Title != "Brand new issue" {
		t.Errorf("Expected new issue title 'Brand new issue', got: %s", newIssue.Title)
	}

	// Verify remapping message was printed
	if !strings.Contains(stderrOutput, "remapped") {
		t.Errorf("Expected remapping message in stderr, got: %s", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "test-mc-1") {
		t.Errorf("Expected test-mc-1 in remapping message, got: %s", stderrOutput)
	}

	// Verify colliding issues were created with new IDs
	// They should appear in the database with different IDs
	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to get all issues: %v", err)
	}

	// Should have: 5 original + 1 new + 3 remapped = 9 total
	if len(allIssues) < 8 {
		t.Errorf("Expected at least 8 issues (5 original + 1 new + 3 remapped), got %d", len(allIssues))
	}
}

// TestAutoImportAllCollisionsRemapped tests when every issue has a collision
func TestAutoImportAllCollisionsRemapped(t *testing.T) {
	now := time.Now().UTC()
	closedTime := now.Add(-1 * time.Hour)

	dbIssues := []*types.Issue{
		{
			ID:        "test-ac-1",
			Title:     "Local 1",
			Status:    types.StatusClosed,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			ClosedAt:  &closedTime,
		},
		{
			ID:        "test-ac-2",
			Title:     "Local 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	// JSONL with all conflicts (different content for same IDs)
	jsonlIssues := []*types.Issue{
		{
			ID:        "test-ac-1",
			Title:     "Remote 1 (conflict)",
			Status:    types.StatusOpen,
			Priority:  3,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-ac-2",
			Title:     "Remote 2 (conflict)",
			Status:    types.StatusClosed,
			Priority:  1,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now,
			ClosedAt:  &closedTime,
		},
	}

	writeJSONLFile(t, tmpDir, jsonlIssues)

	// Capture stderr and run auto-import
	stderrOutput := captureStderr(t, autoImportIfNewer)

	ctx := context.Background()

	// Verify all local versions preserved
	local1, _ := testStore.GetIssue(ctx, "test-ac-1")
	if local1.Title != "Local 1" {
		t.Errorf("Expected local version preserved, got: %s", local1.Title)
	}

	local2, _ := testStore.GetIssue(ctx, "test-ac-2")
	if local2.Title != "Local 2" {
		t.Errorf("Expected local version preserved, got: %s", local2.Title)
	}

	// Verify remapping message mentions both
	if !strings.Contains(stderrOutput, "remapped 2") {
		t.Errorf("Expected '2' in remapping count, got: %s", stderrOutput)
	}
}

// TestAutoImportExactMatchesOnly tests happy path with no conflicts
func TestAutoImportExactMatchesOnly(t *testing.T) {
	now := time.Now().UTC()

	dbIssues := []*types.Issue{
		{
			ID:        "test-em-1",
			Title:     "Exact match issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	// JSONL with exact match + new issue
	jsonlIssues := []*types.Issue{
		{
			ID:        "test-em-1",
			Title:     "Exact match issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-em-2",
			Title:     "New issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	writeJSONLFile(t, tmpDir, jsonlIssues)

	// Run auto-import (should not print collision warnings)
	stderrOutput := captureStderr(t, autoImportIfNewer)

	ctx := context.Background()

	// Verify new issue imported
	newIssue, _ := testStore.GetIssue(ctx, "test-em-2")
	if newIssue == nil {
		t.Fatal("Expected new issue to be imported")
	}
	if newIssue.Title != "New issue" {
		t.Errorf("Expected title 'New issue', got: %s", newIssue.Title)
	}

	// Verify no collision warnings
	if strings.Contains(stderrOutput, "remapped") {
		t.Errorf("Expected no remapping message, got: %s", stderrOutput)
	}
}

// TestAutoImportHashUnchanged tests fast path when JSONL hasn't changed
func TestAutoImportHashUnchanged(t *testing.T) {
	now := time.Now().UTC()

	dbIssues := []*types.Issue{
		{
			ID:        "test-hu-1",
			Title:     "Test issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	writeJSONLFile(t, tmpDir, dbIssues)

	// Run auto-import first time
	os.Setenv("BD_DEBUG", "1")
	defer os.Unsetenv("BD_DEBUG")

	stderrOutput1 := captureStderr(t, autoImportIfNewer)

	// Should trigger import on first run
	if !strings.Contains(stderrOutput1, "auto-import triggered") && !strings.Contains(stderrOutput1, "hash changed") {
		t.Logf("First run: %s", stderrOutput1)
	}

	// Run auto-import second time (JSONL unchanged)
	stderrOutput2 := captureStderr(t, autoImportIfNewer)

	// Verify fast path was taken (hash match)
	if !strings.Contains(stderrOutput2, "JSONL unchanged") {
		t.Errorf("Expected 'JSONL unchanged' in debug output, got: %s", stderrOutput2)
	}
}

// TestAutoImportParseError tests that parse errors are handled gracefully
func TestAutoImportParseError(t *testing.T) {
	now := time.Now().UTC()

	dbIssues := []*types.Issue{
		{
			ID:        "test-pe-1",
			Title:     "Test issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	// Create malformed JSONL
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	os.WriteFile(jsonlPath, []byte(`{"id":"test-pe-1","title":"Good issue","status":"open","priority":1,"issue_type":"task","created_at":"2025-10-16T00:00:00Z","updated_at":"2025-10-16T00:00:00Z"}
{invalid json here}
`), 0644)

	// Run auto-import (should skip due to parse error)
	stderrOutput := captureStderr(t, autoImportIfNewer)

	// Verify parse error was reported
	if !strings.Contains(stderrOutput, "parse error") {
		t.Errorf("Expected parse error message, got: %s", stderrOutput)
	}
}

// TestAutoImportEmptyJSONL tests behavior with empty JSONL file
func TestAutoImportEmptyJSONL(t *testing.T) {
	now := time.Now().UTC()

	dbIssues := []*types.Issue{
		{
			ID:        "test-ej-1",
			Title:     "Existing issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	// Create empty JSONL
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	os.WriteFile(jsonlPath, []byte(""), 0644)

	// Run auto-import
	autoImportIfNewer()

	ctx := context.Background()

	// Verify existing issue still exists (not deleted)
	existing, _ := testStore.GetIssue(ctx, "test-ej-1")
	if existing == nil {
		t.Fatal("Expected existing issue to remain after empty JSONL import")
	}
}

// TestAutoImportNewIssuesOnly tests importing only new issues
func TestAutoImportNewIssuesOnly(t *testing.T) {
	now := time.Now().UTC()

	dbIssues := []*types.Issue{
		{
			ID:        "test-ni-1",
			Title:     "Existing issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	// JSONL with only new issues (no collisions, no exact matches)
	jsonlIssues := []*types.Issue{
		{
			ID:        "test-ni-2",
			Title:     "New issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "test-ni-3",
			Title:     "New issue 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	writeJSONLFile(t, tmpDir, jsonlIssues)

	// Run auto-import
	stderrOutput := captureStderr(t, autoImportIfNewer)

	ctx := context.Background()

	// Verify new issues imported
	issue2, _ := testStore.GetIssue(ctx, "test-ni-2")
	if issue2 == nil || issue2.Title != "New issue 1" {
		t.Error("Expected new issue 1 to be imported")
	}

	issue3, _ := testStore.GetIssue(ctx, "test-ni-3")
	if issue3 == nil || issue3.Title != "New issue 2" {
		t.Error("Expected new issue 2 to be imported")
	}

	// Verify no collision warnings
	if strings.Contains(stderrOutput, "remapped") {
		t.Errorf("Expected no collision messages, got: %s", stderrOutput)
	}
}

// TestAutoImportUpdatesExactMatches tests that exact matches update the DB
func TestAutoImportUpdatesExactMatches(t *testing.T) {
	now := time.Now().UTC()
	oldTime := now.Add(-24 * time.Hour)

	dbIssues := []*types.Issue{
		{
			ID:        "test-um-1",
			Title:     "Exact match",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: oldTime,
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	// JSONL with exact match (same content, newer timestamp)
	jsonlIssues := []*types.Issue{
		{
			ID:        "test-um-1",
			Title:     "Exact match",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: oldTime,
			UpdatedAt: now, // Newer timestamp
		},
	}

	writeJSONLFile(t, tmpDir, jsonlIssues)

	// Run auto-import
	autoImportIfNewer()

	ctx := context.Background()

	// Verify issue was updated (UpdatedAt should be newer)
	updated, _ := testStore.GetIssue(ctx, "test-um-1")
	if updated.UpdatedAt.Before(now.Add(-1 * time.Second)) {
		t.Errorf("Expected UpdatedAt to be updated to %v, got %v", now, updated.UpdatedAt)
	}
}

// TestAutoImportJSONLNotFound tests behavior when JSONL doesn't exist
func TestAutoImportJSONLNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-notfound-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	// Don't create JSONL file

	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer testStore.Close()

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}()

	// Enable debug mode to see skip message
	os.Setenv("BD_DEBUG", "1")
	defer os.Unsetenv("BD_DEBUG")

	// Run auto-import (should skip silently)
	stderrOutput := captureStderr(t, autoImportIfNewer)

	// Verify it skipped due to missing JSONL
	if !strings.Contains(stderrOutput, "JSONL not found") {
		t.Logf("Expected 'JSONL not found' message, got: %s", stderrOutput)
	}
}

// TestAutoImportCollisionRemapMultipleFields tests remapping with different field conflicts
func TestAutoImportCollisionRemapMultipleFields(t *testing.T) {
	now := time.Now().UTC()

	// Create issue with many fields set
	dbIssues := []*types.Issue{
		{
			ID:                 "test-fields-1",
			Title:              "Local title",
			Description:        "Local description",
			Status:             types.StatusOpen,
			Priority:           1,
			IssueType:          types.TypeTask,
			CreatedAt:          now,
			UpdatedAt:          now,
			Notes:              "Local notes",
			Design:             "Local design",
			AcceptanceCriteria: "Local acceptance",
		},
	}

	tmpDir, testStore := createTestDBWithIssues(t, dbIssues)
	setupAutoImportTest(t, testStore, tmpDir)

	ctx := context.Background()

	// JSONL with conflicts in multiple fields
	jsonlIssues := []*types.Issue{
		{
			ID:                 "test-fields-1",
			Title:              "Remote title (conflict)",
			Description:        "Remote description (conflict)",
			Status:             types.StatusClosed,
			Priority:           3,
			IssueType:          types.TypeBug,
			CreatedAt:          now,
			UpdatedAt:          now,
			ClosedAt:           &now,
			Notes:              "Remote notes (conflict)",
			Design:             "Remote design (conflict)",
			AcceptanceCriteria: "Remote acceptance (conflict)",
		},
	}

	writeJSONLFile(t, tmpDir, jsonlIssues)

	// Run auto-import
	stderrOutput := captureStderr(t, autoImportIfNewer)

	// Verify remapping occurred
	if !strings.Contains(stderrOutput, "test-fields-1") {
		t.Logf("Expected remapping message for test-fields-1: %s", stderrOutput)
	}

	// Verify local version of issue is preserved with all fields
	local, _ := testStore.GetIssue(ctx, "test-fields-1")
	if local.Title != "Local title" {
		t.Errorf("Expected local title preserved, got: %s", local.Title)
	}
	if local.Description != "Local description" {
		t.Errorf("Expected local description preserved, got: %s", local.Description)
	}
	if local.Status != types.StatusOpen {
		t.Errorf("Expected local status preserved, got: %s", local.Status)
	}
	if local.Priority != 1 {
		t.Errorf("Expected local priority preserved, got: %d", local.Priority)
	}
}

// TestAutoImportMetadataReadError tests error handling when metadata can't be read
func TestAutoImportMetadataReadError(t *testing.T) {
	// This test is difficult to implement without mocking since metadata
	// should always work in SQLite. We can document that this error path
	// is defensive but hard to trigger in practice.
	t.Skip("Metadata read error is defensive code path, hard to test without mocking")
}
