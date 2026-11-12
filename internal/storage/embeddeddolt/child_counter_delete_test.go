//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDeleteIssueReservesTheFreedChildSuffix is the embedded-store twin of the
// server-backed singular-delete test (internal/storage/dolt): deleting the
// highest child must leave the parent's counter at that suffix, so the next
// --parent mint continues above it (GH#4750). The counter row is dropped by
// hand first — the shape a database written before GH#4750 holds.
func TestDeleteIssueReservesTheFreedChildSuffix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cd")
	ctx := t.Context()

	seed := func(id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	seed("cd-parent")
	for i := 1; i <= 3; i++ {
		seed(fmt.Sprintf("cd-parent.%d", i))
	}
	te.exec(t, ctx, "DELETE FROM child_counters WHERE parent_id = ?", "cd-parent")

	if err := te.store.DeleteIssue(ctx, "cd-parent.3"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	next, err := te.store.GetNextChildID(ctx, "cd-parent")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != "cd-parent.4" {
		t.Fatalf("GetNextChildID = %q, want cd-parent.4: a deleted child's suffix must stay reserved (GH#4750)", next)
	}
}
