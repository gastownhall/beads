package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestDanglingWispEdge_DoctorCountsHeldRows is the read behind the bd doctor
// Blocked State note: issueops.CountIsBlockedHeldInTx counts the open rows the
// recompute leaves as found, and the stale count stays 0 over them. It is 0
// while every wisp is here. Once the wisps are gone it counts each holder of a
// dangling edge, and the child of a parent blocked only through one, and no
// row with a blocker this database can see, and no closed row. The
// any-children waiter is counted although its spawner's open durable child is
// visible: the gate is not read from a partial view of the spawner's children.
func TestDanglingWispEdge_DoctorCountsHeldRows(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Held: blocks on the absent wisp.
	createPerm(t, ctx, store, "dhc-a")
	createWisp(t, ctx, store, "dhc-w")
	addDependencyWithMeta(t, ctx, store, "dhc-a", "dhc-w", types.DepBlocks, "")
	// Not held: a visible open blocker beside the dangling edge.
	createPerm(t, ctx, store, "dhc-b")
	createPerm(t, ctx, store, "dhc-x")
	addDependencyWithMeta(t, ctx, store, "dhc-b", "dhc-w", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dhc-b", "dhc-x", types.DepBlocks, "")
	// Not held: closed.
	createPerm(t, ctx, store, "dhc-z")
	addDependencyWithMeta(t, ctx, store, "dhc-z", "dhc-w", types.DepBlocks, "")
	if err := store.CloseIssue(ctx, "dhc-z", "done", "tester", ""); err != nil {
		t.Fatalf("close dhc-z: %v", err)
	}
	// Held: a parent blocked only through the wisp, and its child.
	createPerm(t, ctx, store, "dhc-p")
	createPerm(t, ctx, store, "dhc-c")
	addDependencyWithMeta(t, ctx, store, "dhc-p", "dhc-w", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dhc-c", "dhc-p", types.DepParentChild, "")
	// Held: an any-children waiter on a wisp spawner, and the spawner's
	// durable child, whose parent-child edge then dangles.
	createPerm(t, ctx, store, "dhc-g")
	createWisp(t, ctx, store, "dhc-s")
	createPerm(t, ctx, store, "dhc-k")
	createWisp(t, ctx, store, "dhc-kw")
	addDependencyWithMeta(t, ctx, store, "dhc-k", "dhc-s", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dhc-kw", "dhc-s", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dhc-g", "dhc-s", types.DepWaitsFor, `{"gate":"any-children"}`)
	if err := store.CloseIssue(ctx, "dhc-kw", "done", "tester", ""); err != nil {
		t.Fatalf("close dhc-kw: %v", err)
	}

	countHeld := func(when string) int64 {
		t.Helper()
		n, err := issueops.CountIsBlockedHeldInTx(ctx, store.db)
		if err != nil {
			t.Fatalf("%s: CountIsBlockedHeldInTx: %v", when, err)
		}
		return n
	}
	if n := countHeld("every wisp here"); n != 0 {
		t.Errorf("every wisp here: held count %d, want 0", n)
	}
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("every wisp here: doctor counts %d stale rows, want 0", n)
	}

	loseWisp(ctx, t, store, "dhc-kw")
	loseWisp(ctx, t, store, "dhc-s")
	loseWisp(ctx, t, store, "dhc-w")
	recomputeAll(ctx, t, store.db)
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("wisps absent, after repair: doctor counts %d stale rows, want 0", n)
	}
	// dhc-a, dhc-p, dhc-c, dhc-g, dhc-k.
	if n := countHeld("wisps absent"); n != 5 {
		t.Errorf("wisps absent: held count %d, want 5", n)
	}
}
