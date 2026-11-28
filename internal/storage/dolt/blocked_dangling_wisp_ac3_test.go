package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// assertBothPathsSet plants a stale value on each id, then requires the full
// repair, and separately the scoped recompute over ids, to set want.
func assertBothPathsSet(ctx context.Context, t *testing.T, store *DoltStore, when string, stale, want map[string]bool, ids ...string) {
	t.Helper()
	for id, v := range stale {
		plantIsBlocked(ctx, t, store, id, v)
	}
	if changed := recomputeAll(ctx, t, store.db); changed != int64(len(stale)) {
		t.Errorf("%s: full repair corrected %d rows, want %d", when, changed, len(stale))
	}
	assertBlockedFlags(ctx, t, store, when+" (after full repair)", want)
	for id, v := range stale {
		plantIsBlocked(ctx, t, store, id, v)
	}
	recomputeScoped(ctx, t, store.db, ids...)
	assertBlockedFlags(ctx, t, store, when+" (after scoped recompute)", want)
	assertConvergedAndStable(ctx, t, store)
}

// TestDanglingWispEdge_VisibleReasonStillMarks: a row holding a dangling wisp
// edge beside a reason this database can see is decided by that reason. The
// hold is on the unmark side only: true or unknown is true.
func TestDanglingWispEdge_VisibleReasonStillMarks(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// An open issue blocker.
	createPerm(t, ctx, store, "dvr-a")
	createPerm(t, ctx, store, "dvr-x")
	// A present open wisp blocker.
	createPerm(t, ctx, store, "dvr-b")
	createWisp(t, ctx, store, "dvr-wo")
	// A present waits-for spawner whose gate blocks: its wisp child is open.
	createPerm(t, ctx, store, "dvr-f")
	createWisp(t, ctx, store, "dvr-s")
	createWisp(t, ctx, store, "dvr-k")
	for _, id := range []string{"dvr-w1", "dvr-w2", "dvr-w3"} {
		createWisp(t, ctx, store, id)
	}
	addDependencyWithMeta(t, ctx, store, "dvr-a", "dvr-x", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dvr-a", "dvr-w1", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dvr-b", "dvr-wo", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dvr-b", "dvr-w2", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dvr-k", "dvr-s", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dvr-f", "dvr-s", types.DepWaitsFor, "")
	addDependencyWithMeta(t, ctx, store, "dvr-f", "dvr-w3", types.DepBlocks, "")
	for _, id := range []string{"dvr-w1", "dvr-w2", "dvr-w3"} {
		loseWisp(ctx, t, store, id)
	}

	blocked := map[string]bool{"dvr-a": true, "dvr-b": true, "dvr-f": true}
	stale := map[string]bool{"dvr-a": false, "dvr-b": false, "dvr-f": false}
	assertBothPathsSet(ctx, t, store, "held row with a visible reason at 0", stale, blocked, "dvr-a", "dvr-b", "dvr-f")
}

// TestDanglingWispEdge_ClosedOrPinnedHolderCleared: a closed or pinned row is
// never blocked, so its stale 1 is cleared even when the only edge it holds
// names a wisp that is not here.
func TestDanglingWispEdge_ClosedOrPinnedHolderCleared(t *testing.T) {
	for _, status := range []string{"closed", "pinned"} {
		t.Run(status, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			createPerm(t, ctx, store, "dcp-a")
			createWisp(t, ctx, store, "dcp-w")
			addDependencyWithMeta(t, ctx, store, "dcp-a", "dcp-w", types.DepBlocks, "")
			loseWisp(ctx, t, store, "dcp-w")
			setStatusByHand(ctx, t, store, "dcp-a", status)

			if n := countInconsistencies(ctx, t, store.db); n != 1 {
				t.Errorf("%s holder with a stale 1: doctor counts %d, want 1", status, n)
			}
			assertBothPathsSet(ctx, t, store, status+" holder at 1",
				map[string]bool{"dcp-a": true}, map[string]bool{"dcp-a": false}, "dcp-a")
		})
	}
}
