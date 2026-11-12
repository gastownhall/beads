package dolt

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestRenameAwayFromAHierarchicalIDKeepsTheSuffixReserved pins the other
// rename half of GH#4750: renaming P.5 to a top-level id must leave the
// parent's counter holding suffix 5, so the next --parent mint is P.6 and
// not a recycled P.5. The children are seeded with direct INSERTs — the
// state an explicit --id create from before the counter fixes leaves behind —
// so at the base the mint reads only the live rows and hands the freed
// suffix straight back.
func TestRenameAwayFromAHierarchicalIDKeepsTheSuffixReserved(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db
	seed := func(id string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	seed("rca-parent")
	for i := 1; i <= 5; i++ {
		seed(fmt.Sprintf("rca-parent.%d", i))
	}

	if err := store.UpdateIssueID(ctx, "rca-parent.5", "rca-freed", &types.Issue{
		ID: "rca-freed", Title: "renamed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "alice"); err != nil {
		t.Fatalf("UpdateIssueID: %v", err)
	}

	next, err := store.GetNextChildID(ctx, "rca-parent")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "rca-parent.6" {
		t.Fatalf("GetNextChildID = %q, want rca-parent.6: the renamed-away suffix must stay reserved (GH#4750)", next)
	}
}
