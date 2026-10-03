package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// countInconsistencies wraps the read-only detection used by the bd doctor
// Blocked State check.
func countInconsistencies(ctx context.Context, t *testing.T, db *sql.DB) int64 {
	t.Helper()
	n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, db)
	if err != nil {
		t.Fatalf("CountIsBlockedInconsistenciesInTx: %v", err)
	}
	return n
}

// recomputeAll wraps the full repair used by 'bd doctor --fix' and returns the
// number of rows it corrected.
func recomputeAll(ctx context.Context, t *testing.T, db *sql.DB) int64 {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin recompute-all tx: %v", err)
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeAllIsBlockedInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit recompute-all tx: %v", err)
	}
	return changed
}

// TestRecomputeAllIsBlocked_RepairsStaleClearedFlag is the bd-6dnrw.37 repair
// path: a row that SHOULD be blocked but whose is_blocked was left at 0 (the
// shape a skipped post-pull recompute leaves behind). Detection must see it,
// the full recompute must fix it, and detection and repair must then agree the
// database is consistent — the lockstep that keeps the COUNT predicate from
// drifting from the recompute SQL.
func TestRecomputeAllIsBlocked_RepairsStaleClearedFlag(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Correct graph via the normal write path: bm-w blocked on open bm-x.
	seedBlockedPair(ctx, t, store, true)
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("precondition: bm-w should be blocked by open bm-x")
	}
	// A correctly-maintained graph has zero inconsistencies.
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("consistent graph: want 0 inconsistencies, got %d", n)
	}

	// Corrupt: clear bm-w's flag directly, with no recompute — exactly what a
	// merge that bypassed the recompute hook leaves behind.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("corrupt is_blocked: %v", err)
	}
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Fatalf("after corruption: want 1 inconsistency, got %d", n)
	}

	// Repair via the full recompute (the always-available path that does not
	// need a pull to advance HEAD).
	if changed := recomputeAll(ctx, t, store.db); changed != 1 {
		t.Fatalf("repair: want 1 row corrected, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("after repair: bm-w must read blocked again")
	}
	// Detection and repair now agree, and the repair is idempotent.
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("after repair: want 0 inconsistencies, got %d", n)
	}
	if again := recomputeAll(ctx, t, store.db); again != 0 {
		t.Fatalf("repair must be idempotent: want 0 on second run, got %d", again)
	}
}

// TestRecomputeAllIsBlocked_ClearsStuckBlockedFlag is the mirror case: a row
// left is_blocked = 1 after its only blocker was closed remotely (a merge that
// bypassed the recompute hook). `bd ready` would keep hiding it; the full
// recompute must clear the flag.
func TestRecomputeAllIsBlocked_ClearsStuckBlockedFlag(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("precondition: bm-w should be blocked by open bm-x")
	}

	// "Merge": the remote closed the blocker; no local recompute ran, so bm-w
	// is stuck is_blocked = 1 with a closed blocker.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("simulate merged close: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("setup: bm-w must still read blocked before the recompute (the stale flag is the bug)")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Fatalf("after merged close: want 1 inconsistency, got %d", n)
	}

	if changed := recomputeAll(ctx, t, store.db); changed != 1 {
		t.Fatalf("repair: want 1 row corrected, got %d", changed)
	}
	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("after repair: bm-w must be unblocked (its only blocker is closed)")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("after repair: want 0 inconsistencies, got %d", n)
	}
}

// TestRecomputeAllIsBlocked_CascadesThroughParentChild verifies the fixpoint:
// is_blocked propagates from a blocked parent to its child across passes. The
// single-pass detection COUNT is a documented lower bound here — it sees only
// the parent on the first pass — but the recompute corrects the whole chain and
// detection reaches 0 once it converges.
func TestRecomputeAllIsBlocked_CascadesThroughParentChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// bm-w blocked on open bm-x; bm-y is a child of bm-w, so bm-y inherits
	// blocked. All maintained by the normal write path.
	seedBlockedPair(ctx, t, store, true)
	child := &types.Issue{ID: "bm-y", Title: "bm-y", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bm-y", DependsOnID: "bm-w", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed parent-child chain')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit chain: %v", err)
	}
	if !isBlocked(ctx, t, store.db, "bm-y") {
		t.Fatal("precondition: child bm-y should inherit blocked from parent bm-w")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("consistent chain: want 0 inconsistencies, got %d", n)
	}

	// Corrupt both the parent and the child to is_blocked = 0.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id IN ('bm-w', 'bm-y')"); err != nil {
		t.Fatalf("corrupt chain: %v", err)
	}
	// Single-pass detection sees only the parent (the child's parent-child
	// reason depends on the parent's still-corrupted flag) — a lower bound.
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Fatalf("after corruption: want 1 (single-pass lower bound), got %d", n)
	}

	// The fixpoint corrects the whole chain across passes.
	if changed := recomputeAll(ctx, t, store.db); changed != 2 {
		t.Fatalf("repair: want 2 rows corrected across passes, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") || !isBlocked(ctx, t, store.db, "bm-y") {
		t.Fatal("after repair: both bm-w and bm-y must read blocked")
	}
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Fatalf("after repair: want 0 inconsistencies, got %d", n)
	}
}

// --- parent-child cascade: only EXOGENOUS blockedness propagates -------------
//
// gastownhall/beads#6506 (wy-3eb07b). The legs used to cascade on the parent's
// is_blocked bit WHATEVER set it, so the "close gate on the epic" idiom — P
// carries blocks edges onto its own children so it cannot close before them —
// darkened the very children P was waiting for. Neither child could reach bd
// ready, so neither could be worked, so neither could close: a permanent lock.
//
// The contract these four cases pin: a parent-child edge propagates only the
// parent's EXOGENOUS blockedness. P is blocked FOR CHILD C iff P has a
// blocking reason whose target is NOT one of P's own parent-child children, or
// P's own parent is (recursively) exogenously blocked. P's OWN is_blocked is
// unchanged throughout — it really does depend on open children.

// addDepSkippingCycleCheck writes a dependency edge past the per-edge cycle
// check. The close-gate topology cannot be built any other way through the
// store: every edge is legal when it is written, but the pair is refused in
// either order once both exist (CheckBlockingHierarchyInTx rejects a blocker
// that is a descendant; CheckDependencyCycleInTx rejects the parent-child edge
// that closes the loop). That is exactly how the shape arrives in the wild —
// one edge at a time, or through import/merge — and SkipCycleCheck is the
// supported way to reproduce it (the whole-graph END GATE is the batch
// caller's obligation, not this fixture's).
func addDepSkippingCycleCheck(ctx context.Context, t *testing.T, store *DoltStore, source, target string, depType types.DependencyType) {
	t.Helper()
	// The store-level AddDependencyWithOptions does not forward SkipCycleCheck
	// (dolt/dependencies.go builds its own AddDependencyOpts); the transaction
	// surface does, and it is the one batch callers use.
	err := store.RunInTransaction(ctx, "test: seed close-gate edge", func(tx storage.Transaction) error {
		return tx.AddDependencyWithOptions(ctx,
			&types.Dependency{IssueID: source, DependsOnID: target, Type: depType},
			"tester", storage.DependencyAddOptions{SkipCycleCheck: true})
	})
	if err != nil {
		t.Fatalf("add dep %s -> %s (%s): %v", source, target, depType, err)
	}
}

// assertBlockedFlags checks is_blocked for a set of ids in one place so a
// failure names every disagreement rather than the first.
func assertBlockedFlags(ctx context.Context, t *testing.T, store *DoltStore, when string, want map[string]bool) {
	t.Helper()
	for id, expected := range want {
		if got := isBlocked(ctx, t, store.db, id); got != expected {
			t.Errorf("%s: %s is_blocked = %v, want %v", when, id, got, expected)
		}
	}
}

// assertConvergedAndStable pins the lockstep for the case at hand: detection
// counts zero, and the full repair agrees by changing nothing.
func assertConvergedAndStable(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("write path left %d inconsistencies; detection and the union must agree", n)
	}
	if changed := recomputeAll(ctx, t, store.db); changed != 0 {
		t.Errorf("full repair changed %d rows over a write-path-consistent graph, want 0", changed)
	}
}

// TestParentChildCascade_ParentBlockedOnlyByItsOwnChildren is the repro. P
// blocks on C1 and C2, which are P's own parent-child children. P stays
// blocked; the children must NOT be darkened, and must be reachable as ready
// work — the property whose absence made the idiom a permanent lock.
func TestParentChildCascade_ParentBlockedOnlyByItsOwnChildren(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{"pc-gate-p", "pc-gate-c1", "pc-gate-c2"} {
		createPerm(t, ctx, store, id)
	}
	// The blocks edges go in while no hierarchy exists yet...
	addDependencyWithMeta(t, ctx, store, "pc-gate-p", "pc-gate-c1", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "pc-gate-p", "pc-gate-c2", types.DepBlocks, "")
	// ...and the hierarchy after, which is what closes the gate on the epic.
	addDepSkippingCycleCheck(ctx, t, store, "pc-gate-c1", "pc-gate-p", types.DepParentChild)
	addDepSkippingCycleCheck(ctx, t, store, "pc-gate-c2", "pc-gate-p", types.DepParentChild)

	assertBlockedFlags(ctx, t, store, "after the write path", map[string]bool{
		"pc-gate-p":  true,  // P really does depend on two open children.
		"pc-gate-c1": false, // ...but the gate must not darken them.
		"pc-gate-c2": false,
	})
	assertConvergedAndStable(ctx, t, store)
	assertBlockedFlags(ctx, t, store, "after the full repair", map[string]bool{
		"pc-gate-p":  true,
		"pc-gate-c1": false,
		"pc-gate-c2": false,
	})

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	inReady := map[string]bool{}
	for _, issue := range ready {
		inReady[issue.ID] = true
	}
	for _, id := range []string{"pc-gate-c1", "pc-gate-c2"} {
		if !inReady[id] {
			t.Errorf("%s missing from ready work: the children of a close-gate epic are exactly what must stay workable", id)
		}
	}
	if inReady["pc-gate-p"] {
		t.Error("pc-gate-p is ready, but it is blocked on two open children")
	}
}

// TestParentChildCascade_ParentBlocksItsOwnGrandchild is the same lock one
// level down, and the case a DIRECT-child reading of "own child" leaves alive
// (the adversarial review's B1 repro).
//
// P blocks on G, which is not P's child but P's GRANDCHILD: pc C -> P,
// pc C2 -> P, pc G -> C. Under a depth-1 test G reads exogenous, so P darkens
// C, so C darkens G — and G is the only row whose close can ever free P. The
// contract is about blockedness originating outside P's own SUBTREE, so the
// whole hierarchy under P must stay bright while P alone stays blocked.
//
// C2 is the sibling control: it is reachable only through P's cascade, so it
// proves the leg was re-evaluated rather than merely skipped for the branch
// the new edge touched.
func TestParentChildCascade_ParentBlocksItsOwnGrandchild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{"pcg-p", "pcg-c", "pcg-c2", "pcg-g"} {
		createPerm(t, ctx, store, id)
	}
	// The gate goes in first, while P and G are unrelated...
	addDependencyWithMeta(t, ctx, store, "pcg-p", "pcg-g", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "pcg-c", "pcg-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pcg-c2", "pcg-p", types.DepParentChild, "")
	// ...and the edge that puts G inside P's subtree comes last, which is what
	// reclassifies P's reason from exogenous to subtree-derived. Nothing but
	// this edge's own seeding can un-darken C and C2.
	addDepSkippingCycleCheck(ctx, t, store, "pcg-g", "pcg-c", types.DepParentChild)

	want := map[string]bool{
		"pcg-p":  true,  // P still depends on an open G.
		"pcg-c":  false, // but nothing under P may be darkened by it,
		"pcg-c2": false,
		"pcg-g":  false, // least of all the row whose close frees P.
	}
	assertBlockedFlags(ctx, t, store, "after the write path", want)
	assertConvergedAndStable(ctx, t, store)
	assertBlockedFlags(ctx, t, store, "after the full repair", want)

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	inReady := map[string]bool{}
	for _, issue := range ready {
		inReady[issue.ID] = true
	}
	if !inReady["pcg-g"] {
		t.Error("pcg-g missing from ready work: the grandchild P waits on is the one row that can end the lock")
	}
	for _, id := range []string{"pcg-c", "pcg-c2"} {
		if !inReady[id] {
			t.Errorf("%s missing from ready work", id)
		}
	}

	// The same answer from a fully inverted plane: the grandchild case needs
	// the fixpoint, not just the write path's incremental seeding.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 - is_blocked"); err != nil {
		t.Fatalf("invert flags: %v", err)
	}
	if changed := recomputeAll(ctx, t, store.db); changed == 0 {
		t.Fatal("repair reported 0 corrections over an inverted plane")
	}
	assertBlockedFlags(ctx, t, store, "after repairing an inverted plane", want)
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("after repair: want 0 inconsistencies, got %d", n)
	}
}

// TestParentChildCascade_ExogenousParentStillCascades is the control: the fix
// narrows the cascade, it does not remove it. P is blocked by an open issue
// that is NOT one of its children, so both children inherit blocked exactly as
// before.
func TestParentChildCascade_ExogenousParentStillCascades(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{"pc-exo-p", "pc-exo-c1", "pc-exo-c2", "pc-exo-x"} {
		createPerm(t, ctx, store, id)
	}
	addDependencyWithMeta(t, ctx, store, "pc-exo-p", "pc-exo-x", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "pc-exo-c1", "pc-exo-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pc-exo-c2", "pc-exo-p", types.DepParentChild, "")

	assertBlockedFlags(ctx, t, store, "after the write path", map[string]bool{
		"pc-exo-x":  false,
		"pc-exo-p":  true,
		"pc-exo-c1": true,
		"pc-exo-c2": true,
	})
	assertConvergedAndStable(ctx, t, store)
	assertBlockedFlags(ctx, t, store, "after the full repair", map[string]bool{
		"pc-exo-p":  true,
		"pc-exo-c1": true,
		"pc-exo-c2": true,
	})
}

// TestParentChildCascade_ThreeLevelChainBothWays pins the recursive clause in
// both directions on one graph.
//
// Exogenous chain: GP is blocked by an outside issue, so GP darkens P and P
// darkens C — including C, whose own parent P has no blocking edge of its own.
// That is the clause "P's own parent-child parent is (recursively)
// exogenously blocked", carried one level per fixpoint pass.
//
// Close-gate chain: GP2 is blocked only by its own child P2. Neither P2 nor
// P2's child C2 may be darkened by it.
func TestParentChildCascade_ThreeLevelChainBothWays(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{
		"pc3-x", "pc3-gp", "pc3-p", "pc3-c",
		"pc3-gp2", "pc3-p2", "pc3-c2",
	} {
		createPerm(t, ctx, store, id)
	}

	// Exogenous: GP -> X (outside), then the two-level hierarchy under GP.
	addDependencyWithMeta(t, ctx, store, "pc3-gp", "pc3-x", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "pc3-p", "pc3-gp", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pc3-c", "pc3-p", types.DepParentChild, "")

	// Close gate: GP2 blocks on P2, which is then made GP2's own child, and C2
	// hangs under P2.
	addDependencyWithMeta(t, ctx, store, "pc3-gp2", "pc3-p2", types.DepBlocks, "")
	addDepSkippingCycleCheck(ctx, t, store, "pc3-p2", "pc3-gp2", types.DepParentChild)
	addDependencyWithMeta(t, ctx, store, "pc3-c2", "pc3-p2", types.DepParentChild, "")

	want := map[string]bool{
		"pc3-x":   false,
		"pc3-gp":  true,
		"pc3-p":   true,
		"pc3-c":   true,
		"pc3-gp2": true,  // blocked on its own open child...
		"pc3-p2":  false, // ...which must not be darkened,
		"pc3-c2":  false, // ...nor anything under it.
	}
	assertBlockedFlags(ctx, t, store, "after the write path", want)
	assertConvergedAndStable(ctx, t, store)
	assertBlockedFlags(ctx, t, store, "after the full repair", want)

	// The full repair reaches the same fixpoint from a fully inverted plane,
	// not just from the write path's answer: the chain is the case where the
	// cascade needs more than one pass.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 - is_blocked"); err != nil {
		t.Fatalf("invert flags: %v", err)
	}
	if changed := recomputeAll(ctx, t, store.db); changed == 0 {
		t.Fatal("repair reported 0 corrections over an inverted plane")
	}
	assertBlockedFlags(ctx, t, store, "after repairing an inverted plane", want)
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("after repair: want 0 inconsistencies, got %d", n)
	}
}

// TestParentChildCascade_WaitsForGateOverOwnChildren pins the same contract for
// the waits-for leg, whose "reason" is a gate rather than a target status.
//
// A gate whose spawner is the parent's OWN child is child-derived and must not
// darken the parent's children; a gate on a spawner outside the hierarchy is
// exogenous and must.
func TestParentChildCascade_WaitsForGateOverOwnChildren(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{
		"pcw-p", "pcw-c1", "pcw-c2", "pcw-g",
		"pcw-p2", "pcw-c3", "pcw-s", "pcw-sc",
	} {
		createPerm(t, ctx, store, id)
	}

	// Own-child gate: P waits for C1, its own child. C1 has an open child of
	// its own, so the all-children gate is live and P is blocked.
	addDependencyWithMeta(t, ctx, store, "pcw-c1", "pcw-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pcw-c2", "pcw-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pcw-g", "pcw-c1", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pcw-p", "pcw-c1", types.DepWaitsFor, "")

	// Outside gate: P2 waits for a spawner that is not in its hierarchy.
	addDependencyWithMeta(t, ctx, store, "pcw-c3", "pcw-p2", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pcw-sc", "pcw-s", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "pcw-p2", "pcw-s", types.DepWaitsFor, "")

	want := map[string]bool{
		"pcw-p":  true,  // its own child's fanout is still open
		"pcw-c1": false, // but the gate is over C1 itself: no darkening
		"pcw-c2": false,
		"pcw-g":  false,
		"pcw-p2": true, // gate on an outside spawner: exogenous
		"pcw-c3": true, // so the child inherits
		"pcw-s":  false,
		"pcw-sc": false,
	}
	assertBlockedFlags(ctx, t, store, "after the write path", want)
	assertConvergedAndStable(ctx, t, store)
	assertBlockedFlags(ctx, t, store, "after the full repair", want)
}

// horizonChain builds "P blocks on a descendant `levels` parent-child edges
// below it" and returns the chain ids, nearest child first.
//
// The order is the only order the store allows: the blocks edge goes in while
// the hierarchy does not exist yet, then the chain from the top down, and the
// LAST edge — the one that finally puts the blocked target inside P's subtree —
// needs SkipCycleCheck, because that is the edge that closes the loop.
func horizonChain(ctx context.Context, t *testing.T, store *DoltStore, prefix string, levels int) []string {
	t.Helper()
	chain := make([]string, 0, levels)
	parent := prefix + "-p"
	createPerm(t, ctx, store, parent)
	for i := 1; i <= levels; i++ {
		id := fmt.Sprintf("%s-d%d", prefix, i)
		createPerm(t, ctx, store, id)
		chain = append(chain, id)
	}

	addDependencyWithMeta(t, ctx, store, parent, chain[levels-1], types.DepBlocks, "")
	for i, id := range chain {
		up := parent
		if i > 0 {
			up = chain[i-1]
		}
		if i == levels-1 {
			addDepSkippingCycleCheck(ctx, t, store, id, up, types.DepParentChild)
			continue
		}
		addDependencyWithMeta(t, ctx, store, id, up, types.DepParentChild, "")
	}
	return chain
}

// TestParentChildCascade_ReasonAtTheWalkHorizon pins the deepest shape the
// exogeneity test actually decides: P blocks on a descendant exactly
// subtreeWalkDepth parent-child edges below it. The walk reaches P on its last
// level, so the reason reads as subtree-derived and nothing under P is
// darkened.
//
// Together with the past-horizon test below, this is the pin on the horizon
// itself: these two shapes differ by one edge and must land on opposite sides.
func TestParentChildCascade_ReasonAtTheWalkHorizon(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// 4 = subtreeWalkDepth (internal/storage/issueops/blocked_consistency.go).
	chain := horizonChain(ctx, t, store, "pch4", 4)

	want := map[string]bool{"pch4-p": true}
	for _, id := range chain {
		want[id] = false
	}
	assertBlockedFlags(ctx, t, store, "after the write path", want)
	assertConvergedAndStable(ctx, t, store)
	assertBlockedFlags(ctx, t, store, "after the full repair", want)

	// And from an inverted plane, which is the leg the fixpoint has to walk.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 - is_blocked"); err != nil {
		t.Fatalf("invert flags: %v", err)
	}
	if changed := recomputeAll(ctx, t, store.db); changed == 0 {
		t.Fatal("repair reported 0 corrections over an inverted plane")
	}
	assertBlockedFlags(ctx, t, store, "after repairing an inverted plane", want)
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("after repair: want 0 inconsistencies, got %d", n)
	}
}

// TestParentChildCascade_ReasonPastTheWalkHorizon is the PIN ON THE
// LIMITATION, not on the fix (gastownhall/beads#6506, fix round 2 / D2).
//
// The ancestor walk in parentsExplainedBySubtreeSQL is four levels deep
// (subtreeWalkDepth), spelled as four pairs of indexed LEFT JOINs because
// WITH RECURSIVE measured 7.5x per evaluation and blew the driver read timeout
// on the full repair. So a blocking reason whose target is FIVE parent-child
// edges below its blocker is out of the walk's reach and reads as EXOGENOUS.
//
// This test states exactly what that costs, so the limitation is a pinned
// behavior rather than a comment: the whole chain under P stays dark, which is
// the pre-#6506 behavior for this one shape. It therefore PASSES UNCHANGED ON
// origin/main — deliberately, and it is the assertion that makes "no new dark
// rows, no new visible rows past the horizon" checkable rather than argued. If
// someone raises the horizon, this test fails and names the row that changed.
//
// The direction matters and is the safe one: past the horizon the cascade
// propagates MORE than the contract asks, never less, so no work is hidden
// that the old code showed. What survives is the old permanent lock for a
// hierarchy deeper than epic -> sub-epic -> leg -> task between a blocker and
// the row it waits on.
func TestParentChildCascade_ReasonPastTheWalkHorizon(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// 5 = subtreeWalkDepth + 1: one level past what the walk can see.
	chain := horizonChain(ctx, t, store, "pch5", 5)

	// Every row in the chain stays blocked, P included: origin/main's answer.
	want := map[string]bool{"pch5-p": true}
	for _, id := range chain {
		want[id] = true
	}
	assertBlockedFlags(ctx, t, store, "after the write path", want)
	assertConvergedAndStable(ctx, t, store)
	assertBlockedFlags(ctx, t, store, "after the full repair", want)

	// No NEW visible row either: nothing in the shape is ready work, which is
	// what it means for the past-horizon answer to be unchanged.
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	for _, issue := range ready {
		if _, ours := want[issue.ID]; ours {
			t.Errorf("%s is ready work: past the walk horizon the cascade is unchanged from origin/main, where the whole chain is dark", issue.ID)
		}
	}

	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 - is_blocked"); err != nil {
		t.Fatalf("invert flags: %v", err)
	}
	if changed := recomputeAll(ctx, t, store.db); changed == 0 {
		t.Fatal("repair reported 0 corrections over an inverted plane")
	}
	assertBlockedFlags(ctx, t, store, "after repairing an inverted plane", want)
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("after repair: want 0 inconsistencies, got %d", n)
	}
}

// TestParentChildCascade_IncrementalMatchesFullRepairAcrossEdits is the
// lockstep proof for the incremental seeding this fix changed
// (AffectedByDepChange{,ForWisp}InTx now walk UP from a parent-child edge's
// target to every reason-carrying ancestor and reseed each ancestor's whole
// sibling set, see appendSiblingsUnderAncestorsInTx).
//
// The property: after EVERY mutation, the flags the incremental write path
// left behind are exactly the flags a full repair computes from scratch —
// detection counts 0 and the repair changes 0 rows (assertConvergedAndStable).
// An under-seeded incremental path shows up here as a nonzero repair on a
// graph the write path just finished maintaining.
//
// The mutations are the ones that reclassify a blocking reason WITHOUT
// touching the row that carries it, which is where seeding is hard to get
// right: moving the reason's target INTO the blocker's subtree, closing and
// reopening it, moving it back OUT, and removing the reason.
//
// Note what is NOT here: adding a blocks edge straight onto an existing own
// descendant. The store refuses that outright (CheckBlockingHierarchyInTx, the
// refusal whose reason text this branch corrects), so the shape can only
// arrive the way it arrives in the wild — one edge at a time, or through
// import/merge — and the re-parent below is the one-edge-at-a-time arrival.
func TestParentChildCascade_IncrementalMatchesFullRepairAcrossEdits(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{"ls-p", "ls-c", "ls-c2", "ls-g", "ls-out"} {
		createPerm(t, ctx, store, id)
	}

	step := func(name string, want map[string]bool) {
		t.Helper()
		assertBlockedFlags(ctx, t, store, name+" (write path)", want)
		// The lockstep itself: detection sees nothing to fix and the full
		// repair changes nothing.
		assertConvergedAndStable(ctx, t, store)
		assertBlockedFlags(ctx, t, store, name+" (after full repair)", want)
	}

	// The starting graph: P blocks on G while G is OUTSIDE P's subtree, so the
	// reason is exogenous and P's two children inherit it. G hangs under a
	// root of its own.
	addDependencyWithMeta(t, ctx, store, "ls-p", "ls-g", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "ls-c", "ls-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "ls-c2", "ls-p", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "ls-g", "ls-out", types.DepParentChild, "")

	exogenous := map[string]bool{
		"ls-p": true, "ls-c": true, "ls-c2": true, "ls-g": false, "ls-out": false,
	}
	subtreeDerived := map[string]bool{
		"ls-p": true, "ls-c": false, "ls-c2": false, "ls-g": false, "ls-out": false,
	}
	clear := map[string]bool{
		"ls-p": false, "ls-c": false, "ls-c2": false, "ls-g": false, "ls-out": false,
	}
	step("exogenous blocker outside the subtree", exogenous)

	// 1. Re-parent G INTO P's subtree, under C. The same blocks edge is now a
	// close gate over P's own grandchild, so P keeps its flag and its whole
	// subtree must come bright — including C and C2, which this edge does not
	// name. SkipCycleCheck is the only way in: P blocks G, so pc G -> C closes
	// a loop.
	if err := store.RemoveDependency(ctx, "ls-g", "ls-out", "tester"); err != nil {
		t.Fatalf("remove parent-child ls-g -> ls-out: %v", err)
	}
	addDepSkippingCycleCheck(ctx, t, store, "ls-g", "ls-c", types.DepParentChild)
	step("reason re-parented into the subtree", subtreeDerived)

	// 2. Close the descendant: a closed target is no reason at all, so P comes
	// bright with the edge still in place. Then reopen it.
	if err := store.CloseIssue(ctx, "ls-g", "lockstep", "tester", ""); err != nil {
		t.Fatalf("close ls-g: %v", err)
	}
	step("descendant closed", clear)
	if err := store.ReopenIssue(ctx, "ls-g", "lockstep", "tester"); err != nil {
		t.Fatalf("reopen ls-g: %v", err)
	}
	step("descendant reopened", subtreeDerived)

	// 3. Re-parent G back OUT. The reason turns exogenous again and P's
	// cascade comes back on for C and C2.
	if err := store.RemoveDependency(ctx, "ls-g", "ls-c", "tester"); err != nil {
		t.Fatalf("remove parent-child ls-g -> ls-c: %v", err)
	}
	addDependencyWithMeta(t, ctx, store, "ls-g", "ls-out", types.DepParentChild, "")
	step("reason re-parented out of the subtree", exogenous)

	// 4. Remove the reason itself: nothing anywhere carries one.
	if err := store.RemoveDependency(ctx, "ls-p", "ls-g", "tester"); err != nil {
		t.Fatalf("remove blocks ls-p -> ls-g: %v", err)
	}
	step("blocks edge removed", clear)
}
