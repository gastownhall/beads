package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestLabelAndCommentWritesLeaveCleanWorkingSet verifies that AddLabel,
// RemoveLabel, AddComment, and AddIssueComment each create a Dolt commit
// so the working set is clean afterwards. Before the fix, server-mode bd
// writes accumulated as uncommitted rows on the live sql-server's working
// set, blocking subsequent remotesapi pushes from other clients with
// "target has uncommitted changes" until a manual DOLT_COMMIT was issued.
func TestLabelAndCommentWritesLeaveCleanWorkingSet(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "flush-1",
		Title:     "label/comment commit hygiene",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// CreateIssue itself commits — confirm the baseline is clean.
	assertWorkingSetClean(t, store, "after CreateIssue")

	if err := store.AddLabel(ctx, issue.ID, "bug", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	assertWorkingSetClean(t, store, "after AddLabel")

	if err := store.RemoveLabel(ctx, issue.ID, "bug", "tester"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	assertWorkingSetClean(t, store, "after RemoveLabel")

	if err := store.AddComment(ctx, issue.ID, "tester", "drive-by note"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	assertWorkingSetClean(t, store, "after AddComment")

	if _, err := store.AddIssueComment(ctx, issue.ID, "tester", "structured comment"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	assertWorkingSetClean(t, store, "after AddIssueComment")
}

// assertWorkingSetClean fails the test if any non-ignored table is dirty
// in the Dolt working set. dolt_ignore'd tables (wisps, local_metadata,
// repo_mtimes) are intentionally excluded — they never commit by design.
func assertWorkingSetClean(t *testing.T, store *DoltStore, label string) {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT s.table_name FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`)
	if err != nil {
		t.Fatalf("%s: query dolt_status: %v", label, err)
	}
	defer rows.Close()
	var dirty []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("%s: scan dolt_status: %v", label, err)
		}
		dirty = append(dirty, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: iterate dolt_status: %v", label, err)
	}
	if len(dirty) > 0 {
		t.Errorf("%s: working set has uncommitted tables %v (expected clean)", label, dirty)
	}
}
