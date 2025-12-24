//go:build integration
// +build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
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

func TestBulkDeleteNoResurrection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	testDB := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	testGitInit(t, tmpDir)

	s := newTestStore(t, testDB)
	ctx := context.Background()

	totalIssues := 20
	toDeleteCount := 10
	var toDelete []string

	for i := 1; i <= totalIssues; i++ {
		issue := &types.Issue{
			Title:       "Issue " + string(rune('A'+i-1)),
			Description: "Test issue",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   "task",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
		if i <= toDeleteCount {
			toDelete = append(toDelete, issue.ID)
		}
	}

	exportToJSONLTest(t, s, jsonlPath)
	testGitCommit(t, tmpDir, jsonlPath, "Add issues")

	oldStore := store
	oldDbPath := dbPath
	oldAutoImportEnabled := autoImportEnabled
	defer func() {
		store = oldStore
		dbPath = oldDbPath
		autoImportEnabled = oldAutoImportEnabled
	}()

	store = s
	dbPath = testDB
	autoImportEnabled = true

	result, err := s.DeleteIssues(ctx, toDelete, false, true, false)
	if err != nil {
		t.Fatalf("DeleteIssues failed: %v", err)
	}

	if result.DeletedCount != toDeleteCount {
		t.Errorf("Expected %d deletions, got %d", toDeleteCount, result.DeletedCount)
	}

	for _, id := range toDelete {
		if err := removeIssueFromJSONL(id); err != nil {
			t.Fatalf("removeIssueFromJSONL failed for %s: %v", id, err)
		}
	}

	stats, err := s.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	expectedRemaining := totalIssues - toDeleteCount
	if stats.TotalIssues != expectedRemaining {
		t.Errorf("After delete: expected %d issues in DB, got %d", expectedRemaining, stats.TotalIssues)
	}

	jsonlIssues := countJSONLIssuesTest(t, jsonlPath)
	if jsonlIssues != expectedRemaining {
		t.Errorf("After delete: expected %d issues in JSONL, got %d", expectedRemaining, jsonlIssues)
	}

	for _, id := range toDelete {
		issue, err := s.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue failed for %s: %v", id, err)
		}
		if issue != nil {
			t.Errorf("Deleted issue %s was resurrected!", id)
		}
	}
}

func exportToJSONLTest(t *testing.T, s *sqlite.SQLiteStorage, jsonlPath string) {
	t.Helper()
	ctx := context.Background()
	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		t.Fatalf("Failed to create JSONL dir: %v", err)
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, iss := range issues {
		if err := enc.Encode(iss); err != nil {
			t.Fatalf("Failed to encode issue: %v", err)
		}
	}
}

func testGitInit(t *testing.T, dir string) {
	t.Helper()
	testGitCmd(t, dir, "init")
	testGitCmd(t, dir, "config", "user.email", "test@example.com")
	testGitCmd(t, dir, "config", "user.name", "Test User")
}

func testGitCommit(t *testing.T, dir, file, msg string) {
	t.Helper()
	testGitCmd(t, dir, "add", file)
	testGitCmd(t, dir, "commit", "-m", msg)
}

func testGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, output)
	}
}

func countJSONLIssuesTest(t *testing.T, jsonlPath string) int {
	t.Helper()
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("Failed to read JSONL: %v", err)
	}

	count := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if len(bytes.TrimSpace([]byte(line))) > 0 {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("Scanner error: %v", err)
	}
	return count
}

// TestCreateTombstoneWrapper tests the createTombstone wrapper function
func TestCreateTombstoneWrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	testDB := filepath.Join(beadsDir, "beads.db")

	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Save and restore global store
	oldStore := store
	defer func() { store = oldStore }()
	store = s

	t.Run("successful tombstone creation", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Test Issue",
			Description: "Issue to be tombstoned",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   "task",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		err := createTombstone(ctx, issue.ID, "test-actor", "Test deletion reason")
		if err != nil {
			t.Fatalf("createTombstone failed: %v", err)
		}

		// Verify tombstone status
		updated, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if updated == nil {
			t.Fatal("Issue should still exist as tombstone")
		}
		if updated.Status != types.StatusTombstone {
			t.Errorf("Expected status %s, got %s", types.StatusTombstone, updated.Status)
		}
	})

	t.Run("tombstone with actor and reason tracking", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Issue with tracking",
			Description: "Check actor/reason",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   "bug",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		actor := "admin-user"
		reason := "Duplicate issue"
		err := createTombstone(ctx, issue.ID, actor, reason)
		if err != nil {
			t.Fatalf("createTombstone failed: %v", err)
		}

		// Verify actor and reason were recorded
		updated, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if updated.DeletedBy != actor {
			t.Errorf("Expected DeletedBy %q, got %q", actor, updated.DeletedBy)
		}
		if updated.DeleteReason != reason {
			t.Errorf("Expected DeleteReason %q, got %q", reason, updated.DeleteReason)
		}
	})

	t.Run("error when issue does not exist", func(t *testing.T) {
		err := createTombstone(ctx, "nonexistent-issue-id", "actor", "reason")
		if err == nil {
			t.Error("Expected error for non-existent issue")
		}
	})

	t.Run("verify tombstone preserves original type", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Feature issue",
			Description: "Should preserve type",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeFeature,
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		err := createTombstone(ctx, issue.ID, "actor", "reason")
		if err != nil {
			t.Fatalf("createTombstone failed: %v", err)
		}

		updated, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if updated.OriginalType != string(types.TypeFeature) {
			t.Errorf("Expected OriginalType %q, got %q", types.TypeFeature, updated.OriginalType)
		}
	})

	t.Run("verify audit trail recorded", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Issue for audit",
			Description: "Check event recording",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   "task",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		err := createTombstone(ctx, issue.ID, "audit-actor", "audit-reason")
		if err != nil {
			t.Fatalf("createTombstone failed: %v", err)
		}

		// Verify an event was recorded
		events, err := s.GetEvents(ctx, issue.ID, 100)
		if err != nil {
			t.Fatalf("GetEvents failed: %v", err)
		}

		found := false
		for _, e := range events {
			if e.EventType == "deleted" && e.Actor == "audit-actor" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected 'deleted' event in audit trail")
		}
	})
}

// TestDeleteIssueWrapper tests the deleteIssue wrapper function
func TestDeleteIssueWrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	testDB := filepath.Join(beadsDir, "beads.db")

	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Save and restore global store
	oldStore := store
	defer func() { store = oldStore }()
	store = s

	t.Run("successful issue deletion", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Issue to delete",
			Description: "Will be permanently deleted",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   "task",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		err := deleteIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("deleteIssue failed: %v", err)
		}

		// Verify issue is gone
		deleted, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if deleted != nil {
			t.Error("Issue should be completely deleted")
		}
	})

	t.Run("error on non-existent issue", func(t *testing.T) {
		err := deleteIssue(ctx, "nonexistent-issue-id")
		if err == nil {
			t.Error("Expected error for non-existent issue")
		}
	})

	t.Run("verify dependencies are removed", func(t *testing.T) {
		// Create two issues with a dependency
		issue1 := &types.Issue{
			Title:     "Blocker issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: "task",
		}
		issue2 := &types.Issue{
			Title:     "Dependent issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: "task",
		}
		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}

		// Add dependency: issue2 depends on issue1
		dep := &types.Dependency{
			IssueID:     issue2.ID,
			DependsOnID: issue1.ID,
			Type:        types.DepBlocks,
		}
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		// Delete issue1 (the blocker)
		err := deleteIssue(ctx, issue1.ID)
		if err != nil {
			t.Fatalf("deleteIssue failed: %v", err)
		}

		// Verify issue2 no longer has dependencies
		deps, err := s.GetDependencies(ctx, issue2.ID)
		if err != nil {
			t.Fatalf("GetDependencies failed: %v", err)
		}
		if len(deps) > 0 {
			t.Errorf("Expected no dependencies after deleting blocker, got %d", len(deps))
		}
	})

	t.Run("verify issue removed from database", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Verify removal",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: "task",
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Get statistics before delete
		statsBefore, err := s.GetStatistics(ctx)
		if err != nil {
			t.Fatalf("GetStatistics failed: %v", err)
		}

		err = deleteIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("deleteIssue failed: %v", err)
		}

		// Get statistics after delete
		statsAfter, err := s.GetStatistics(ctx)
		if err != nil {
			t.Fatalf("GetStatistics failed: %v", err)
		}

		if statsAfter.TotalIssues != statsBefore.TotalIssues-1 {
			t.Errorf("Expected total issues to decrease by 1, was %d now %d",
				statsBefore.TotalIssues, statsAfter.TotalIssues)
		}
	})
}

func TestCreateTombstoneUnsupportedStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	oldStore := store
	defer func() { store = oldStore }()

	// Set store to nil - the type assertion will fail
	store = nil

	ctx := context.Background()
	err := createTombstone(ctx, "any-id", "actor", "reason")
	if err == nil {
		t.Error("Expected error when storage is nil")
	}
	expectedMsg := "tombstone operation not supported by this storage backend"
	if err.Error() != expectedMsg {
		t.Errorf("Expected error %q, got %q", expectedMsg, err.Error())
	}
}

func TestDeleteIssueUnsupportedStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	oldStore := store
	defer func() { store = oldStore }()

	// Set store to nil - the type assertion will fail
	store = nil

	ctx := context.Background()
	err := deleteIssue(ctx, "any-id")
	if err == nil {
		t.Error("Expected error when storage is nil")
	}
	expectedMsg := "delete operation not supported by this storage backend"
	if err.Error() != expectedMsg {
		t.Errorf("Expected error %q, got %q", expectedMsg, err.Error())
	}
}
