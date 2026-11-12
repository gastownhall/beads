//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestRenameAwayFromAHierarchicalIDKeepsTheSuffixReserved is the embedded-store
// twin of the server-backed rename-away test (internal/storage/dolt):
// renaming P.5 to a top-level id must leave the parent's counter holding
// suffix 5, so the next --parent mint is P.6 and not a recycled P.5
// (GH#4750). The children are seeded with direct INSERTs — the state an
// explicit --id create from before the counter fixes leaves behind.
func TestRenameAwayFromAHierarchicalIDKeepsTheSuffixReserved(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ra")
	ctx := t.Context()

	seed := func(id string) {
		t.Helper()
		te.exec(t, ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id)
	}
	seed("ra-parent")
	for i := 1; i <= 5; i++ {
		seed(fmt.Sprintf("ra-parent.%d", i))
	}

	if err := te.store.UpdateIssueID(ctx, "ra-parent.5", "ra-freed", &types.Issue{
		ID: "ra-freed", Title: "renamed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "alice"); err != nil {
		t.Fatalf("UpdateIssueID: %v", err)
	}

	next, err := te.store.GetNextChildID(ctx, "ra-parent")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "ra-parent.6" {
		t.Fatalf("GetNextChildID = %q, want ra-parent.6: the renamed-away suffix must stay reserved (GH#4750)", next)
	}
}
