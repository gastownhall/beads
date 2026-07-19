//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestHistory_NullTextColumns reproduces GH#4867: a schema migration that
// recreates a column leaves NULL in dolt_history_issues for pre-migration
// commits, which used to crash the scan. We relax the NOT NULL constraint
// and write NULL directly instead of replaying a real migration.
func TestHistory_NullTextColumns(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "nh")
	ctx := t.Context()

	issue := &types.Issue{
		ID:                 "nh-null1",
		Title:              "Null history test",
		Description:        "original description",
		Design:             "original design",
		AcceptanceCriteria: "original AC",
		Notes:              "original notes",
		Status:             types.StatusOpen,
		Priority:           2,
		IssueType:          types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := te.store.Commit(ctx, "initial commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	for _, col := range []string{"description", "design", "acceptance_criteria", "notes"} {
		te.exec(t, ctx, "ALTER TABLE issues MODIFY COLUMN `"+col+"` TEXT")
	}
	te.exec(t, ctx,
		"UPDATE issues SET description = NULL, design = NULL, acceptance_criteria = NULL, notes = NULL WHERE id = ?",
		issue.ID)
	if err := te.store.Commit(ctx, "null text columns commit"); err != nil {
		t.Fatalf("Commit (null columns): %v", err)
	}

	history, err := te.store.History(ctx, issue.ID)
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
