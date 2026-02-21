//go:build cgo

package dolt

import (
	"strings"
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

// TestCommitPending tests the batch commit mechanism.
func TestCommitPending(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Initial commit so the store has a clean HEAD
	if err := store.Commit(ctx, "initial state"); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}

	t.Run("returns false when nothing to commit", func(t *testing.T) {
		committed, err := store.CommitPending(ctx, "test-actor")
		if err != nil {
			t.Fatalf("CommitPending failed: %v", err)
		}
		if committed {
			t.Error("expected false when no changes pending")
		}
	})

	t.Run("commits accumulated changes with summary", func(t *testing.T) {
		headBefore, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("failed to get HEAD: %v", err)
		}

		// Create an issue (DML without Dolt commit)
		issue := &types.Issue{
			Title:     "Batch test issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Now commit pending changes
		committed, err := store.CommitPending(ctx, "test-actor")
		if err != nil {
			t.Fatalf("CommitPending failed: %v", err)
		}
		if !committed {
			t.Error("expected true when changes were pending")
		}

		headAfter, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("failed to get HEAD after commit: %v", err)
		}
		if headAfter == headBefore {
			t.Error("expected HEAD to advance after CommitPending")
		}
	})

	t.Run("generates descriptive message", func(t *testing.T) {
		// Create another issue to have pending changes
		issue := &types.Issue{
			Title:     "Message test issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Build the message (without committing)
		msg := store.buildBatchCommitMessage(ctx, "test-actor")
		if !strings.Contains(msg, "batch commit") {
			t.Errorf("expected 'batch commit' in message, got: %q", msg)
		}
		if !strings.Contains(msg, "test-actor") {
			t.Errorf("expected actor in message, got: %q", msg)
		}
		if !strings.Contains(msg, "created") {
			t.Errorf("expected 'created' in message for new issues, got: %q", msg)
		}

		// Clean up — commit to clear working set
		if err := store.Commit(ctx, "cleanup"); err != nil {
			t.Fatalf("cleanup commit failed: %v", err)
		}
	})
}
