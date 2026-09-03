package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// migration0049DDL replays migration 0049's exact column widening: the four
// issues text columns move from TEXT to LONGTEXT NOT NULL. See
// internal/storage/schema/cli_migrations.go (cliMigration0049LongtextLargeContentColumns).
const migration0049DDL = "ALTER TABLE issues " +
	"MODIFY COLUMN description LONGTEXT NOT NULL, " +
	"MODIFY COLUMN design LONGTEXT NOT NULL, " +
	"MODIFY COLUMN acceptance_criteria LONGTEXT NOT NULL, " +
	"MODIFY COLUMN notes LONGTEXT NOT NULL"

// alterIssuesTextColumnsDown simulates the pre-0049 schema by narrowing the
// four issues text columns from LONGTEXT back to TEXT, without touching any
// row data.
func alterIssuesTextColumnsDown(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	const ddl = "ALTER TABLE issues " +
		"MODIFY COLUMN description TEXT NOT NULL, " +
		"MODIFY COLUMN design TEXT NOT NULL, " +
		"MODIFY COLUMN acceptance_criteria TEXT NOT NULL, " +
		"MODIFY COLUMN notes TEXT NOT NULL"
	if _, err := store.db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("failed to narrow issues text columns to TEXT: %v", err)
	}
}

// TestHistory_NullTextColumns reproduces GH#4867: dolt_history_issues
// projects every historical row against the CURRENT branch-head schema. A
// row committed while the issues text columns were still TEXT (pre-0049)
// type-mismatches the post-0049 LONGTEXT column definition when Dolt
// re-projects it, which surfaces as NULL rather than the original value.
// This is real migration behavior, not a hand-written NULL row: schema
// widening never mutates existing row bytes, only the type Dolt uses to
// project them.
func TestHistory_NullTextColumns(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// (a) Simulate the pre-0049 schema: TEXT columns, not yet migrated.
	alterIssuesTextColumnsDown(t, ctx, store)
	if err := store.Commit(ctx, "narrow issues text columns to TEXT (pre-0049 schema)"); err != nil {
		t.Fatalf("failed to commit TEXT schema: %v", err)
	}

	// (b) Commit an issue under the pre-0049 TEXT schema. This becomes the
	// OLDER history entry.
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
	if err := store.Commit(ctx, "initial commit under TEXT schema"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// (c) Replay migration 0049's exact DDL, widening to LONGTEXT. The row
	// data is untouched; only the branch-head column type changes. This
	// becomes the NEWEST history entry.
	if _, err := store.db.ExecContext(ctx, migration0049DDL); err != nil {
		t.Fatalf("failed to replay migration 0049 DDL: %v", err)
	}
	if err := store.Commit(ctx, "replay migration 0049 (TEXT -> LONGTEXT)"); err != nil {
		t.Fatalf("failed to commit migration 0049: %v", err)
	}

	history, err := store.History(ctx, issue.ID)
	if err != nil {
		t.Fatalf("History() failed across a TEXT -> LONGTEXT migration: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least 2 history entries, got %d", len(history))
	}

	// Newest entry (post-migration commit): schema matches branch head, so
	// the real values project through untouched.
	newest := history[0].Issue
	if newest.Description != issue.Description {
		t.Errorf("expected newest description %q, got %q", issue.Description, newest.Description)
	}
	if newest.Design != issue.Design {
		t.Errorf("expected newest design %q, got %q", issue.Design, newest.Design)
	}
	if newest.AcceptanceCriteria != issue.AcceptanceCriteria {
		t.Errorf("expected newest acceptance_criteria %q, got %q", issue.AcceptanceCriteria, newest.AcceptanceCriteria)
	}
	if newest.Notes != issue.Notes {
		t.Errorf("expected newest notes %q, got %q", issue.Notes, newest.Notes)
	}

	// Older entry (pre-migration commit, TEXT-era): re-projected against the
	// current LONGTEXT schema, the type mismatch surfaces as NULL, which the
	// COALESCE in the scan turns into "".
	older := history[1].Issue
	if older.Description != "" {
		t.Errorf("expected pre-migration description to coalesce to \"\", got %q", older.Description)
	}
	if older.Design != "" {
		t.Errorf("expected pre-migration design to coalesce to \"\", got %q", older.Design)
	}
	if older.AcceptanceCriteria != "" {
		t.Errorf("expected pre-migration acceptance_criteria to coalesce to \"\", got %q", older.AcceptanceCriteria)
	}
	if older.Notes != "" {
		t.Errorf("expected pre-migration notes to coalesce to \"\", got %q", older.Notes)
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

// TestIsSafeCommitRef is a be-shbed / PR #5806 review regression test.
// dolt_diff() accepts the literal "WORKING" as an endpoint alongside real
// commit hashes, and the fix threads that literal through ChangedIssueIDs as
// the incremental path's "to" endpoint (in place of a root/working-set hash
// dolt_diff always rejected) — isSafeCommitRef's character/length check must
// keep admitting it, since no prior test in this file covered that value.
func TestIsSafeCommitRef(t *testing.T) {
	valid := []string{
		"WORKING",
		"a",
		"0123456789abcdefABCDEF",
		strings.Repeat("a", 64),
	}
	for _, s := range valid {
		if !isSafeCommitRef(s) {
			t.Errorf("isSafeCommitRef(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", 65),
		"has space",
		"has-dash",
		"has_underscore",
		"has.dot",
		"'; DROP TABLE issues; --",
		"abc\ndef",
	}
	for _, s := range invalid {
		if isSafeCommitRef(s) {
			t.Errorf("isSafeCommitRef(%q) = true, want false", s)
		}
	}
}
