//go:build cgo

package embeddeddolt_test

import (
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

// TestHistory_NullTextColumns reproduces GH#4867: dolt_history_issues
// projects every historical row against the CURRENT branch-head schema. A
// row committed while the issues text columns were still TEXT (pre-0049)
// type-mismatches the post-0049 LONGTEXT column definition when Dolt
// re-projects it, which surfaces as NULL rather than the original value.
// This is real migration behavior, not a hand-written NULL row: schema
// widening never mutates existing row bytes, only the type Dolt uses to
// project them.
func TestHistory_NullTextColumns(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "nh")
	ctx := t.Context()

	// (a) Simulate the pre-0049 schema: TEXT columns, not yet migrated.
	for _, col := range []string{"description", "design", "acceptance_criteria", "notes"} {
		te.exec(t, ctx, "ALTER TABLE issues MODIFY COLUMN `"+col+"` TEXT NOT NULL")
	}
	if err := te.store.Commit(ctx, "narrow issues text columns to TEXT (pre-0049 schema)"); err != nil {
		t.Fatalf("Commit (TEXT schema): %v", err)
	}

	// (b) Commit an issue under the pre-0049 TEXT schema. This becomes the
	// OLDER history entry.
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
	if err := te.store.Commit(ctx, "initial commit under TEXT schema"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// (c) Replay migration 0049's exact DDL, widening to LONGTEXT. The row
	// data is untouched; only the branch-head column type changes. This
	// becomes the NEWEST history entry.
	te.exec(t, ctx, migration0049DDL)
	if err := te.store.Commit(ctx, "replay migration 0049 (TEXT -> LONGTEXT)"); err != nil {
		t.Fatalf("Commit (migration 0049): %v", err)
	}

	history, err := te.store.History(ctx, issue.ID)
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
