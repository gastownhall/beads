package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// nullOutTextColumns drops the NOT NULL constraint on the given issues text
// columns and sets them to NULL for issueID, reproducing the NULL rows
// dolt_history_issues can carry after a schema migration recreates a column
// (GH#4867), without needing to replay a real migration.
func nullOutTextColumns(t *testing.T, ctx context.Context, store *DoltStore, issueID string, cols ...string) {
	t.Helper()
	for _, col := range cols {
		if _, err := store.db.ExecContext(ctx, "ALTER TABLE issues MODIFY COLUMN `"+col+"` TEXT"); err != nil {
			t.Fatalf("failed to relax %s constraint: %v", col, err)
		}
	}
	sets := make([]string, len(cols))
	for i, col := range cols {
		sets[i] = col + " = NULL"
	}
	stmt := "UPDATE issues SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	if _, err := store.db.ExecContext(ctx, stmt, issueID); err != nil {
		t.Fatalf("failed to null out columns %v: %v", cols, err)
	}
}

// TestHistory_NullTextColumns reproduces GH#4867: a schema migration that
// recreates a column leaves NULL in dolt_history_issues for pre-migration
// commits, which used to crash the scan. We relax the NOT NULL constraint
// and write NULL directly instead of replaying a real migration.
func TestHistory_NullTextColumns(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:                 "null-hist-1",
		Title:              "Null history test",
		Description:        "original description",
		Design:             "original design",
		AcceptanceCriteria: "original AC",
		Notes:              "original notes",
		Status:             types.StatusOpen,
		Priority:           2,
		IssueType:          types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.Commit(ctx, "initial commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	nullOutTextColumns(t, ctx, store, issue.ID, "description", "design", "acceptance_criteria", "notes")
	if err := store.Commit(ctx, "null text columns commit"); err != nil {
		t.Fatalf("failed to commit NULL text columns: %v", err)
	}

	history, err := store.History(ctx, issue.ID)
	if err != nil {
		t.Fatalf("History() failed on NULL text columns: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least 2 history entries, got %d", len(history))
	}

	latest := history[0].Issue
	if latest.Description != "" {
		t.Errorf("expected NULL description to coalesce to \"\", got %q", latest.Description)
	}
	if latest.Design != "" {
		t.Errorf("expected NULL design to coalesce to \"\", got %q", latest.Design)
	}
	if latest.AcceptanceCriteria != "" {
		t.Errorf("expected NULL acceptance_criteria to coalesce to \"\", got %q", latest.AcceptanceCriteria)
	}
	if latest.Notes != "" {
		t.Errorf("expected NULL notes to coalesce to \"\", got %q", latest.Notes)
	}
}

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

		// Insert directly via SQL to leave changes uncommitted in Dolt working set.
		// (CreateIssue auto-commits via DOLT_COMMIT, so it can't be used here.)
		_, err = store.db.ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
			 VALUES ('batch-test-1', 'Batch test issue', '', '', '', '', 'open', 2, 'task', NOW(), NOW())`)
		if err != nil {
			t.Fatalf("raw INSERT failed: %v", err)
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
		// Insert directly via SQL to leave changes uncommitted in Dolt working set.
		// (CreateIssue auto-commits via DOLT_COMMIT, so it can't be used here.)
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
			 VALUES ('msg-test-1', 'Message test issue', '', '', '', '', 'open', 2, 'task', NOW(), NOW())`)
		if err != nil {
			t.Fatalf("raw INSERT failed: %v", err)
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
