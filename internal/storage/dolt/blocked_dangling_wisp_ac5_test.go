package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDanglingWispEdge_IsAnExogenousReasonForChildren: P blocks on its own
// child C, and on a wisp. While the wisp is here and open P is blocked from
// outside its subtree, so C is dark. With the wisp's row gone the only reason
// this database can see is the one inside P's subtree, which alone would
// explain P and brighten C. The dangling edge keeps P unexplained, so C stays
// as the minting database computed it.
func TestDanglingWispEdge_IsAnExogenousReasonForChildren(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dwx-p")
	createPerm(t, ctx, store, "dwx-c")
	createWisp(t, ctx, store, "dwx-w")
	addDependencyWithMeta(t, ctx, store, "dwx-p", "dwx-w", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dwx-p", "dwx-c", types.DepBlocks, "")
	addDepSkippingCycleCheck(ctx, t, store, "dwx-c", "dwx-p", types.DepParentChild)
	dark := map[string]bool{"dwx-p": true, "dwx-c": true}
	assertBlockedFlags(ctx, t, store, "wisp present and open", dark)
	assertConvergedAndStable(ctx, t, store)

	loseWisp(ctx, t, store, "dwx-w")
	assertLeftAsFound(ctx, t, store, "wisp absent", dark, "dwx-p", "dwx-c")
}

// The gate-parent shapes of the review of gastownhall/beads#8597. A parent P
// that blocks on its own child C, and on a wisp, is blocked for C from outside
// its subtree or not depending on where that wisp sits, and a database without
// the wisp cannot tell: the wisp's own parent-child row lives in
// wisp_dependencies, which no clone has. So P is undecided for C, and C keeps
// the flag the minting database gave it, bright as well as dark. Each test
// computes the minting database's flags with the wisp present, loses the wisp
// (loseWisp, which takes the wisp's own wisp_dependencies rows with it, as a
// clone has none), and requires every recompute to leave them alone.
// TestDanglingWispEdge_IsAnExogenousReasonForChildren is the dark twin of A.

// A: P's wisp blocker is closed but not yet purged on the minting database, so
// P's only open reason is its own child and C is bright.
func TestDanglingWispEdge_GateParentOverAClosedWispLeavesChildBright(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dga-p")
	createPerm(t, ctx, store, "dga-c")
	createWisp(t, ctx, store, "dga-w")
	addDependencyWithMeta(t, ctx, store, "dga-p", "dga-w", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dga-p", "dga-c", types.DepBlocks, "")
	addDepSkippingCycleCheck(ctx, t, store, "dga-c", "dga-p", types.DepParentChild)
	if err := store.CloseIssue(ctx, "dga-w", "done", "tester", ""); err != nil {
		t.Fatalf("close wisp: %v", err)
	}
	minting := map[string]bool{"dga-p": true, "dga-c": false}
	assertBlockedFlags(ctx, t, store, "minting database, wisp closed", minting)
	assertConvergedAndStable(ctx, t, store)

	loseWisp(ctx, t, store, "dga-w")
	assertLeftAsFound(ctx, t, store, "wisp absent", minting, "dga-p", "dga-c")
	recomputeScoped(ctx, t, store.db, "dga-c")
	assertBlockedFlags(ctx, t, store, "scoped recompute of the child alone", minting)
}

// B: durable P is a child of an open wisp WP and blocks on its own child C.
// WP is blocked from outside (dark arm) or not at all (bright arm), so on the
// minting database C is dark or bright with it. Without WP the database
// cannot see which: P holds a parent-child edge onto a wisp that is not here.
func TestDanglingWispEdge_GateParentUnderAnAbsentWispParent(t *testing.T) {
	for _, arm := range []struct {
		name, prefix string
		dark         bool
	}{
		{"bright", "dgb", false},
		{"dark", "dgd", true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			// x is the outside blocker that darkens wp in the dark arm.
			wp, p, c, x := arm.prefix+"-wp", arm.prefix+"-p", arm.prefix+"-c", arm.prefix+"-x"

			createWisp(t, ctx, store, wp)
			createPerm(t, ctx, store, p)
			createPerm(t, ctx, store, c)
			if arm.dark {
				createPerm(t, ctx, store, x)
				addDependencyWithMeta(t, ctx, store, wp, x, types.DepBlocks, "")
			}
			addDependencyWithMeta(t, ctx, store, p, wp, types.DepParentChild, "")
			addDependencyWithMeta(t, ctx, store, p, c, types.DepBlocks, "")
			addDepSkippingCycleCheck(ctx, t, store, c, p, types.DepParentChild)
			minting := map[string]bool{p: true, c: arm.dark}
			assertBlockedFlags(ctx, t, store, "minting database", minting)
			assertConvergedAndStable(ctx, t, store)

			loseWisp(ctx, t, store, wp)
			assertLeftAsFound(ctx, t, store, "wisp parent absent", minting, p, c)
			recomputeScoped(ctx, t, store.db, c)
			assertBlockedFlags(ctx, t, store, "scoped recompute of the child alone", minting)
		})
	}
}

// D: P closes over a mix of children, a durable C and an open wisp W, and
// blocks on both. On the minting database both reasons are inside P's
// subtree, so C is bright. Without W the database sees an edge onto a wisp it
// cannot place, and must not darken C on the guess that W is outside.
func TestDanglingWispEdge_GateParentOverAnOpenWispChildLeavesChildBright(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dgw-p")
	createPerm(t, ctx, store, "dgw-c")
	createWisp(t, ctx, store, "dgw-w")
	addDependencyWithMeta(t, ctx, store, "dgw-p", "dgw-c", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dgw-p", "dgw-w", types.DepBlocks, "")
	addDepSkippingCycleCheck(ctx, t, store, "dgw-c", "dgw-p", types.DepParentChild)
	addDepSkippingCycleCheck(ctx, t, store, "dgw-w", "dgw-p", types.DepParentChild)
	minting := map[string]bool{"dgw-p": true, "dgw-c": false}
	assertBlockedFlags(ctx, t, store, "minting database, wisp child open", minting)
	assertConvergedAndStable(ctx, t, store)

	loseWisp(ctx, t, store, "dgw-w")
	assertLeftAsFound(ctx, t, store, "wisp child absent", minting, "dgw-p", "dgw-c")
}

// An undecided parent decides nothing below it either: Q blocks on a wisp and
// P, a close gate on its own child C, hangs under Q. On the minting database
// C is dark when the wisp is outside Q's subtree (Q is blocked from outside, so
// P is blocked from above) and bright when it is Q's own wisp child (Q is a
// close gate too). P carries no dependency on a wisp itself, so only the walk
// above it can find that it is undecided. The scoped recompute of C alone
// reaches Q through the by-id read of the parents above the batch.
func TestDanglingWispEdge_UndecidedAncestorLeavesAGateParentsChildAsFound(t *testing.T) {
	for _, arm := range []struct {
		name   string
		inside bool
		prefix string
	}{
		{"wisp outside the ancestor's subtree", false, "dqo"},
		{"wisp inside the ancestor's subtree", true, "dqi"},
	} {
		t.Run(arm.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			q, p, c, w := arm.prefix+"-q", arm.prefix+"-p", arm.prefix+"-c", arm.prefix+"-w"

			createPerm(t, ctx, store, q)
			createPerm(t, ctx, store, p)
			createPerm(t, ctx, store, c)
			createWisp(t, ctx, store, w)
			addDependencyWithMeta(t, ctx, store, q, w, types.DepBlocks, "")
			addDependencyWithMeta(t, ctx, store, p, c, types.DepBlocks, "")
			addDependencyWithMeta(t, ctx, store, p, q, types.DepParentChild, "")
			addDepSkippingCycleCheck(ctx, t, store, c, p, types.DepParentChild)
			if arm.inside {
				addDepSkippingCycleCheck(ctx, t, store, w, q, types.DepParentChild)
			}
			minting := map[string]bool{q: true, p: true, c: !arm.inside}
			assertBlockedFlags(ctx, t, store, "minting database", minting)
			assertConvergedAndStable(ctx, t, store)

			loseWisp(ctx, t, store, w)
			assertLeftAsFound(ctx, t, store, "wisp absent", minting, q, p, c)
			recomputeScoped(ctx, t, store.db, c)
			assertBlockedFlags(ctx, t, store, "scoped recompute of the child alone", minting)
		})
	}
}

// TestDanglingWispEdge_HeldParentDecidesNothingBelowIt: P holds a dangling
// wisp edge and nothing else, and its child C has no reason of its own. P's
// flag and its transparency are both unknown here, so C keeps its flag at 1
// and at 0, and a stored 1 on P marks nothing on C. When P also has a reason
// this database can see outside its own subtree, P is blocked from outside
// and C is set to 1; a closed or pinned C is cleared whatever P holds.
func TestDanglingWispEdge_HeldParentDecidesNothingBelowIt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dhp-p")
	createPerm(t, ctx, store, "dhp-c")
	createWisp(t, ctx, store, "dhp-w")
	addDependencyWithMeta(t, ctx, store, "dhp-p", "dhp-w", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dhp-c", "dhp-p", types.DepParentChild, "")
	loseWisp(ctx, t, store, "dhp-w")

	// P's stored 1 marks nothing on C.
	plantIsBlocked(ctx, t, store, "dhp-p", true)
	plantIsBlocked(ctx, t, store, "dhp-c", false)
	assertLeftAsFound(ctx, t, store, "P at 1, C at 0",
		map[string]bool{"dhp-p": true, "dhp-c": false}, "dhp-p", "dhp-c")
	plantIsBlocked(ctx, t, store, "dhp-c", true)
	assertLeftAsFound(ctx, t, store, "P at 1, C at 1",
		map[string]bool{"dhp-p": true, "dhp-c": true}, "dhp-p", "dhp-c")

	// A closed or pinned child is never blocked.
	for _, status := range []string{"closed", "pinned"} {
		setStatusByHand(ctx, t, store, "dhp-c", status)
		plantIsBlocked(ctx, t, store, "dhp-c", true)
		if changed := recomputeAll(ctx, t, store.db); changed != 1 {
			t.Errorf("%s child at 1: full repair corrected %d rows, want 1", status, changed)
		}
		assertBlockedFlags(ctx, t, store, status+" child (after full repair)", map[string]bool{"dhp-c": false})
		plantIsBlocked(ctx, t, store, "dhp-c", true)
		recomputeScoped(ctx, t, store.db, "dhp-c")
		assertBlockedFlags(ctx, t, store, status+" child (after scoped recompute)", map[string]bool{"dhp-c": false})
	}
	setStatusByHand(ctx, t, store, "dhp-c", "open")
	plantIsBlocked(ctx, t, store, "dhp-c", false)

	// A reason outside P's subtree decides P, and with it C.
	createPerm(t, ctx, store, "dhp-x")
	addDependencyWithMeta(t, ctx, store, "dhp-p", "dhp-x", types.DepBlocks, "")
	assertBothPathsSet(ctx, t, store, "P blocked from outside",
		map[string]bool{"dhp-c": false}, map[string]bool{"dhp-p": true, "dhp-c": true}, "dhp-p", "dhp-c")
}

// TestDanglingWispEdge_ReasonlessHeldChildPassesItsFlagDown pins where the hold
// stops (the review of gastownhall/beads#8639). P blocks on a wisp that is not
// here, C is P's child and G is C's, and neither child has a reason of its own.
// C is undecided: P's flag and transparency both hang on the wisp. G is not: C
// carries no reason row, so on every database C's flag is inherited and passes
// to G unchanged. G is therefore decided from the flag C holds as found, which
// is the minting database's value for C, and that database derives the same G
// from it. Both recomputes keep C as found and set G to C's flag, at 1 and at
// 0, and the doctor counts G as stale until they do.
func TestDanglingWispEdge_ReasonlessHeldChildPassesItsFlagDown(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "drg-p")
	createPerm(t, ctx, store, "drg-c")
	createPerm(t, ctx, store, "drg-g")
	createWisp(t, ctx, store, "drg-w")
	addDependencyWithMeta(t, ctx, store, "drg-p", "drg-w", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "drg-c", "drg-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "drg-g", "drg-c", types.DepParentChild, "")
	dark := map[string]bool{"drg-p": true, "drg-c": true, "drg-g": true}
	assertBlockedFlags(ctx, t, store, "minting database, wisp open", dark)
	assertConvergedAndStable(ctx, t, store)

	loseWisp(ctx, t, store, "drg-w")
	assertLeftAsFound(ctx, t, store, "wisp absent", dark, "drg-p", "drg-c", "drg-g")

	// G follows the 1 that C holds.
	plantIsBlocked(ctx, t, store, "drg-g", false)
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Errorf("G at 0 under C at 1: doctor counts %d stale rows, want 1", n)
	}
	assertBothPathsSet(ctx, t, store, "G at 0 under C at 1",
		map[string]bool{"drg-g": false}, dark, "drg-g")

	// C keeps a merged 0 (the minting value when the wisp sits inside P's
	// subtree), and G follows that too.
	bright := map[string]bool{"drg-p": true, "drg-c": false, "drg-g": false}
	plantIsBlocked(ctx, t, store, "drg-c", false)
	plantIsBlocked(ctx, t, store, "drg-g", false)
	assertLeftAsFound(ctx, t, store, "C at 0", bright, "drg-p", "drg-c", "drg-g")
	plantIsBlocked(ctx, t, store, "drg-g", true)
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Errorf("G at 1 under C at 0: doctor counts %d stale rows, want 1", n)
	}
	assertBothPathsSet(ctx, t, store, "G at 1 under C at 0",
		map[string]bool{"drg-g": true}, bright, "drg-g")
}
