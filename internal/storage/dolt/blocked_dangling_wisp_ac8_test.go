package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDanglingWispEdge_LocalMarkOutlivesItsBlocker pins a trade-off rather
// than an ideal (probe C of the review of gastownhall/beads#8597). An undecided
// row keeps the flag it has whoever set it, and is_blocked carries no record
// of who did. Here the merged 0 is replaced by a local blocker's 1 on
// evidence, and when that blocker closes the 1 stays: nothing this database
// can see says whether the absent wisp still holds the row. It clears when a
// merge brings the minting database's 0, or when the dangling edge goes.
func TestDanglingWispEdge_LocalMarkOutlivesItsBlocker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dlm-a")
	createPerm(t, ctx, store, "dlm-x")
	createWisp(t, ctx, store, "dlm-w")
	addDependencyWithMeta(t, ctx, store, "dlm-a", "dlm-w", types.DepBlocks, "")
	if err := store.CloseIssue(ctx, "dlm-w", "done", "tester", ""); err != nil {
		t.Fatalf("close wisp: %v", err)
	}
	assertBlockedFlags(ctx, t, store, "wisp closed", map[string]bool{"dlm-a": false})
	loseWisp(ctx, t, store, "dlm-w")

	addDependencyWithMeta(t, ctx, store, "dlm-a", "dlm-x", types.DepBlocks, "")
	assertBlockedFlags(ctx, t, store, "a local blocker marks on evidence", map[string]bool{"dlm-a": true})
	if err := store.CloseIssue(ctx, "dlm-x", "done", "tester", ""); err != nil {
		t.Fatalf("close local blocker: %v", err)
	}
	assertLeftAsFound(ctx, t, store, "local blocker closed", map[string]bool{"dlm-a": true}, "dlm-a")

	// Removing the dangling edge decides the row again.
	if err := store.RemoveDependency(ctx, "dlm-a", "dlm-w", "tester"); err != nil {
		t.Fatalf("remove dangling edge: %v", err)
	}
	recomputeScoped(ctx, t, store.db, "dlm-a")
	assertBlockedFlags(ctx, t, store, "dangling edge removed", map[string]bool{"dlm-a": false})
	assertConvergedAndStable(ctx, t, store)
}
