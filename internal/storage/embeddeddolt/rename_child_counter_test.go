//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestRenameIntoAHierarchicalIDRaisesTheParentCounter is the embedded-store
// twin of the server-backed rename test (internal/storage/dolt): renaming a
// top-level row into a hierarchical id P.9 must raise P's child counter to
// cover the suffix the renamed row now holds, so the next --parent mint
// cannot collide with it (GH#4750).
func TestRenameIntoAHierarchicalIDRaisesTheParentCounter(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rn")
	ctx := t.Context()

	seed := func(id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	seed("rn-parent")
	for i := 1; i <= 3; i++ {
		seed(fmt.Sprintf("rn-parent.%d", i))
	}
	seed("rn-top")

	if err := te.store.UpdateIssueID(ctx, "rn-top", "rn-parent.9", &types.Issue{
		ID: "rn-parent.9", Title: "renamed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "alice"); err != nil {
		t.Fatalf("UpdateIssueID: %v", err)
	}

	var lastChild int
	te.queryScalar(t, ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?",
		[]any{"rn-parent"}, &lastChild)
	if lastChild < 9 {
		t.Fatalf("child_counters.last_child for rn-parent = %d, want >= 9 after renaming rn-top into rn-parent.9", lastChild)
	}
}
