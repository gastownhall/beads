//go:build cgo && integration
// +build cgo,integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

	oldStore := store
	oldDbPath := dbPath
	defer func() {
		store = oldStore
		dbPath = oldDbPath
	}()

	store = s
	dbPath = testDB

	result, err := s.DeleteIssues(ctx, toDelete, false, true, false)
	if err != nil {
		t.Fatalf("DeleteIssues failed: %v", err)
	}

	if result.DeletedCount != toDeleteCount {
		t.Errorf("Expected %d deletions, got %d", toDeleteCount, result.DeletedCount)
	}

	stats, err := s.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	expectedRemaining := totalIssues - toDeleteCount
	if stats.TotalIssues != expectedRemaining {
		t.Errorf("After delete: expected %d issues in DB, got %d", expectedRemaining, stats.TotalIssues)
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

func testGitInit(t *testing.T, dir string) {
	t.Helper()
	testGitCmd(t, dir, "init")
	testGitCmd(t, dir, "config", "user.email", "test@example.com")
	testGitCmd(t, dir, "config", "user.name", "Test User")
}

func testGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, output)
	}
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

func TestDeleteIssueUnsupportedStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test container not available")
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
