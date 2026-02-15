//go:build cgo

package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCommitExists tests the CommitExists method.
func TestCommitExists(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Get the current commit hash (should exist after store initialization)
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("failed to get current commit: %v", err)
	}

	t.Run("valid commit hash returns true", func(t *testing.T) {
		exists, err := store.CommitExists(ctx, currentCommit)
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if !exists {
			t.Errorf("expected commit %s to exist", currentCommit)
		}
	})

	t.Run("short hash prefix returns true", func(t *testing.T) {
		// Use first 8 characters as a short hash (like git's default short SHA)
		if len(currentCommit) < 8 {
			t.Skip("commit hash too short for prefix test")
		}
		shortHash := currentCommit[:8]
		exists, err := store.CommitExists(ctx, shortHash)
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if !exists {
			t.Errorf("expected short hash %s to match commit %s", shortHash, currentCommit)
		}
	})

	t.Run("invalid nonexistent commit returns false", func(t *testing.T) {
		exists, err := store.CommitExists(ctx, "0000000000000000000000000000000000000000")
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if exists {
			t.Error("expected nonexistent commit to return false")
		}
	})

	t.Run("empty string returns false", func(t *testing.T) {
		exists, err := store.CommitExists(ctx, "")
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if exists {
			t.Error("expected empty string to return false")
		}
	})

	t.Run("malformed input returns false", func(t *testing.T) {
		testCases := []string{
			"invalid hash with spaces",
			"hash'with'quotes",
			"hash;injection",
			"hash--comment",
		}
		for _, tc := range testCases {
			exists, err := store.CommitExists(ctx, tc)
			if err != nil {
				t.Fatalf("CommitExists(%q) returned error: %v", tc, err)
			}
			if exists {
				t.Errorf("expected malformed input %q to return false", tc)
			}
		}
	})
}

// TestGetChangesSinceExport tests the GetChangesSinceExport method.
func TestGetChangesSinceExport(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	t.Run("empty commit returns needsFullExport", func(t *testing.T) {
		result, err := store.GetChangesSinceExport(ctx, "")
		if err != nil {
			t.Fatalf("GetChangesSinceExport failed: %v", err)
		}
		if !result.NeedsFullExport {
			t.Error("expected NeedsFullExport=true for empty commit")
		}
	})

	t.Run("invalid commit returns needsFullExport", func(t *testing.T) {
		result, err := store.GetChangesSinceExport(ctx, "nonexistent123456789012345678901234567890")
		if err != nil {
			t.Fatalf("GetChangesSinceExport failed: %v", err)
		}
		if !result.NeedsFullExport {
			t.Error("expected NeedsFullExport=true for invalid commit")
		}
	})

	t.Run("malformed commit returns needsFullExport", func(t *testing.T) {
		result, err := store.GetChangesSinceExport(ctx, "invalid'hash")
		if err != nil {
			t.Fatalf("GetChangesSinceExport failed: %v", err)
		}
		if !result.NeedsFullExport {
			t.Error("expected NeedsFullExport=true for malformed commit")
		}
	})

	t.Run("no changes returns empty entries", func(t *testing.T) {
		// First create and commit an issue so the issues table has committed data
		issue := &types.Issue{
			ID:          "test-export-baseline",
			Title:       "Baseline Issue",
			Description: "Ensures table exists in committed state",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create baseline issue: %v", err)
		}
		if err := store.Commit(ctx, "Add baseline issue"); err != nil {
			t.Fatalf("failed to commit baseline: %v", err)
		}

		// Get current commit
		currentCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("failed to get current commit: %v", err)
		}

		// Query changes since current commit (should be none)
		result, err := store.GetChangesSinceExport(ctx, currentCommit)
		if err != nil {
			t.Fatalf("GetChangesSinceExport failed: %v", err)
		}
		if result.NeedsFullExport {
			t.Error("expected NeedsFullExport=false for valid commit")
		}
		if len(result.Entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(result.Entries))
		}
	})

	t.Run("create issue shows added in diff", func(t *testing.T) {
		// Get commit before creating issue
		beforeCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("failed to get current commit: %v", err)
		}

		// Create an issue
		issue := &types.Issue{
			ID:          "test-export-add",
			Title:       "Test Export Add",
			Description: "Testing added detection",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Commit the changes
		if err := store.Commit(ctx, "Add test issue"); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Get changes since before
		result, err := store.GetChangesSinceExport(ctx, beforeCommit)
		if err != nil {
			t.Fatalf("GetChangesSinceExport failed: %v", err)
		}

		if result.NeedsFullExport {
			t.Error("expected NeedsFullExport=false")
		}

		// Find the added entry
		var foundAdded bool
		for _, entry := range result.Entries {
			if entry.IssueID == issue.ID && entry.DiffType == "added" {
				foundAdded = true
				if entry.NewValue == nil {
					t.Error("expected NewValue to be set for added entry")
				}
				break
			}
		}
		if !foundAdded {
			t.Errorf("expected to find 'added' entry for issue %s", issue.ID)
		}
	})

	t.Run("update issue shows modified in diff", func(t *testing.T) {
		// Create an issue first
		issue := &types.Issue{
			ID:          "test-export-modify",
			Title:       "Test Export Modify",
			Description: "Testing modified detection",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
		if err := store.Commit(ctx, "Add issue for modify test"); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Get commit before updating
		beforeCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("failed to get current commit: %v", err)
		}

		// Update the issue
		updates := map[string]interface{}{
			"title": "Updated Title",
		}
		if err := store.UpdateIssue(ctx, issue.ID, updates, "tester"); err != nil {
			t.Fatalf("failed to update issue: %v", err)
		}
		if err := store.Commit(ctx, "Update test issue"); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Get changes since before
		result, err := store.GetChangesSinceExport(ctx, beforeCommit)
		if err != nil {
			t.Fatalf("GetChangesSinceExport failed: %v", err)
		}

		if result.NeedsFullExport {
			t.Error("expected NeedsFullExport=false")
		}

		// Find the modified entry
		var foundModified bool
		for _, entry := range result.Entries {
			if entry.IssueID == issue.ID && entry.DiffType == "modified" {
				foundModified = true
				if entry.OldValue == nil || entry.NewValue == nil {
					t.Error("expected both OldValue and NewValue to be set for modified entry")
				}
				break
			}
		}
		if !foundModified {
			t.Errorf("expected to find 'modified' entry for issue %s", issue.ID)
		}
	})

	t.Run("delete issue shows removed in diff", func(t *testing.T) {
		// Create an issue first
		issue := &types.Issue{
			ID:          "test-export-delete",
			Title:       "Test Export Delete",
			Description: "Testing removed detection",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
		if err := store.Commit(ctx, "Add issue for delete test"); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Get commit before deleting
		beforeCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("failed to get current commit: %v", err)
		}

		// Delete the issue
		if err := store.DeleteIssue(ctx, issue.ID); err != nil {
			t.Fatalf("failed to delete issue: %v", err)
		}
		if err := store.Commit(ctx, "Delete test issue"); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Get changes since before
		result, err := store.GetChangesSinceExport(ctx, beforeCommit)
		if err != nil {
			t.Fatalf("GetChangesSinceExport failed: %v", err)
		}

		if result.NeedsFullExport {
			t.Error("expected NeedsFullExport=false")
		}

		// Find the removed entry
		var foundRemoved bool
		for _, entry := range result.Entries {
			if entry.IssueID == issue.ID && entry.DiffType == "removed" {
				foundRemoved = true
				if entry.OldValue == nil {
					t.Error("expected OldValue to be set for removed entry")
				}
				break
			}
		}
		if !foundRemoved {
			t.Errorf("expected to find 'removed' entry for issue %s", issue.ID)
		}
	})
}
