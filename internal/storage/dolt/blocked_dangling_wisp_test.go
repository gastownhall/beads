package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Tests for bd-muhnf0 / gastownhall/beads#8597: a dependency row that names a
// wisp this database has no row for. dependencies is versioned and its
// depends_on_wisp_id replicates; wisps and wisp_% are dolt_ignored and do not.
// A clone therefore holds the edge and not the wisp. Such an edge is UNKNOWN:
// the rows whose value hangs on it are held at the flag they were found with,
// unless something this database can see decides them.

// loseWisp deletes a wisp's row by hand and leaves every dependency that names
// it, which is the state of a clone: dependencies is versioned and replicates
// with its depends_on_wisp_id, the wisps table is dolt_ignored and does not.
// The store's own delete would remove the edges with the wisp.
func loseWisp(ctx context.Context, t *testing.T, store *DoltStore, id string) {
	t.Helper()
	res, err := store.db.ExecContext(ctx, "DELETE FROM wisps WHERE id = ?", id)
	if err != nil {
		t.Fatalf("delete wisp %s by hand: %v", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("delete wisp %s by hand: %d rows, err %v; want 1", id, n, err)
	}
}

// plantIsBlocked sets a row's flag by hand, as a merge that brought the other
// database's answer would.
func plantIsBlocked(ctx context.Context, t *testing.T, store *DoltStore, id string, blocked bool) {
	t.Helper()
	v := 0
	if blocked {
		v = 1
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = ? WHERE id = ?", v, id); err != nil {
		t.Fatalf("plant is_blocked = %d on %s: %v", v, id, err)
	}
}

// setStatusByHand sets a row's status the way a merge would, with no write
// path (and no recompute) running.
func setStatusByHand(ctx context.Context, t *testing.T, store *DoltStore, id, status string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = ? WHERE id = ?", status, id); err != nil {
		t.Fatalf("set status %s on %s: %v", status, id, err)
	}
}

// commitAll commits the working set, so the guarded store repair runs.
func commitAll(ctx context.Context, t *testing.T, store *DoltStore, msg string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", msg); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit %q: %v", msg, err)
	}
}

// assertLeftAsFound runs the doctor count, the full repair and the scoped
// recompute over ids, and requires all three to leave the flags alone.
func assertLeftAsFound(ctx context.Context, t *testing.T, store *DoltStore, when string, want map[string]bool, ids ...string) {
	t.Helper()
	if n := countInconsistencies(ctx, t, store.db); n != 0 {
		t.Errorf("%s: doctor counts %d stale rows, want 0", when, n)
	}
	if changed := recomputeAll(ctx, t, store.db); changed != 0 {
		t.Errorf("%s: full repair changed %d rows, want 0", when, changed)
	}
	assertBlockedFlags(ctx, t, store, when+" (after full repair)", want)
	recomputeScoped(ctx, t, store.db, ids...)
	assertBlockedFlags(ctx, t, store, when+" (after scoped recompute)", want)
}

// TestDanglingWispEdge_FlagLeftAsFound is the clone's case: an issue blocks on
// a wisp whose row is not in this database. Neither value of is_blocked can be
// derived here, so the full repair, the scoped recompute a pull runs and the
// doctor count all leave the flag as the last merge set it, at 1 and at 0.
// Before the fix the absent wisp read as a satisfied blocker and both
// recomputes cleared the 1.
func TestDanglingWispEdge_FlagLeftAsFound(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		kind         types.DependencyType
	}{
		{"blocks", "dwb", types.DepBlocks},
		{"conditional-blocks", "dwq", types.DepConditionalBlocks},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			a, w := tc.prefix+"-a", tc.prefix+"-w"

			createPerm(t, ctx, store, a)
			createWisp(t, ctx, store, w)
			addDependencyWithMeta(t, ctx, store, a, w, tc.kind, "")
			if !isBlocked(ctx, t, store.db, a) {
				t.Fatalf("precondition: %s should be blocked by open wisp %s", a, w)
			}

			loseWisp(ctx, t, store, w)
			assertLeftAsFound(ctx, t, store, "flag 1, wisp absent", map[string]bool{a: true}, a)

			// The minting database closed the wisp and the merge brought its 0.
			plantIsBlocked(ctx, t, store, a, false)
			assertLeftAsFound(ctx, t, store, "flag 0, wisp absent", map[string]bool{a: false}, a)
		})
	}
}

// TestDanglingWispEdge_ParentChildToAbsentWisp: a child whose parent-child
// parent is a wisp that is not here keeps its inherited flag, as a blocks edge
// does.
func TestDanglingWispEdge_ParentChildToAbsentWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dwc-x")
	createWisp(t, ctx, store, "dwc-p")
	createPerm(t, ctx, store, "dwc-c")
	addDependencyWithMeta(t, ctx, store, "dwc-p", "dwc-x", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dwc-c", "dwc-p", types.DepParentChild, "")
	if !isBlocked(ctx, t, store.db, "dwc-c") {
		t.Fatal("precondition: dwc-c should inherit from its blocked wisp parent")
	}

	loseWisp(ctx, t, store, "dwc-p")
	assertLeftAsFound(ctx, t, store, "inherited 1, wisp parent absent", map[string]bool{"dwc-c": true}, "dwc-c")
	plantIsBlocked(ctx, t, store, "dwc-c", false)
	assertLeftAsFound(ctx, t, store, "inherited 0, wisp parent absent", map[string]bool{"dwc-c": false}, "dwc-c")
}

// TestDanglingWispEdge_WaitsForAbsentSpawner: a waits-for gate reads its
// spawner's children, and a wisp spawner's wisp children are no more here than
// the spawner is. With both rows gone the gate reads "no open child", which
// would clear the waiter; the waiter keeps its flag instead, at 1 and at 0.
func TestDanglingWispEdge_WaitsForAbsentSpawner(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dwf-a")
	createWisp(t, ctx, store, "dwf-s")
	createWisp(t, ctx, store, "dwf-k")
	addDependencyWithMeta(t, ctx, store, "dwf-k", "dwf-s", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dwf-a", "dwf-s", types.DepWaitsFor, "")
	if !isBlocked(ctx, t, store.db, "dwf-a") {
		t.Fatal("precondition: dwf-a should wait for wisp spawner dwf-s while its child dwf-k is open")
	}

	loseWisp(ctx, t, store, "dwf-k")
	loseWisp(ctx, t, store, "dwf-s")
	assertLeftAsFound(ctx, t, store, "waiting, spawner absent", map[string]bool{"dwf-a": true}, "dwf-a")
	plantIsBlocked(ctx, t, store, "dwf-a", false)
	assertLeftAsFound(ctx, t, store, "released, spawner absent", map[string]bool{"dwf-a": false}, "dwf-a")
}

// TestDanglingWispEdge_WaitsForAbsentAnyChildrenSpawnerWithVisibleOpenChild:
// an any-children gate onto a wisp spawner S, with one durable child K (its
// parent-child row lives in dependencies, so it replicates) still open and one
// wisp child closed. On the minting database the closed child opens the gate,
// so the waiter is clear. A clone sees K open and not the closed wisp child,
// and reading the gate from that partial view would block the waiter. The
// gate is non-monotone in its children, so a visible open child is not a
// visible reason: the waiter keeps its flag, at 0 and at 1.
func TestDanglingWispEdge_WaitsForAbsentAnyChildrenSpawnerWithVisibleOpenChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dwy-a")
	createWisp(t, ctx, store, "dwy-s")
	createPerm(t, ctx, store, "dwy-k")
	createWisp(t, ctx, store, "dwy-kw")
	addDependencyWithMeta(t, ctx, store, "dwy-k", "dwy-s", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dwy-kw", "dwy-s", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dwy-a", "dwy-s", types.DepWaitsFor, `{"gate":"any-children"}`)
	if err := store.CloseIssue(ctx, "dwy-kw", "done", "tester", ""); err != nil {
		t.Fatalf("close wisp child: %v", err)
	}
	minting := map[string]bool{"dwy-a": false, "dwy-k": false}
	assertBlockedFlags(ctx, t, store, "minting database, one child closed", minting)
	assertConvergedAndStable(ctx, t, store)

	loseWisp(ctx, t, store, "dwy-kw")
	loseWisp(ctx, t, store, "dwy-s")
	assertLeftAsFound(ctx, t, store, "released, spawner absent", minting, "dwy-a", "dwy-k")
	plantIsBlocked(ctx, t, store, "dwy-a", true)
	assertLeftAsFound(ctx, t, store, "waiting, spawner absent",
		map[string]bool{"dwy-a": true, "dwy-k": false}, "dwy-a", "dwy-k")
}

// TestRecomputeAllBlocked_DanglingWispEdgeMintsNoCommit runs the store's full
// repair (bd recompute-blocked; bd doctor --fix reaches the same
// RecomputeAllIsBlockedInTx) over a clone holding one row per edge type that
// names an absent wisp, at 1 and then at 0. It corrects nothing, so it mints
// no "bd: recompute is_blocked (full)" commit: HEAD does not move. Before the
// fix every such repair wrote the guess and committed it, which is what the
// minting database then wrote back on the next merge.
func TestRecomputeAllBlocked_DanglingWispEdgeMintsNoCommit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dnc-x")
	holders := []string{"dnc-b", "dnc-q", "dnc-c", "dnc-f"}
	for _, id := range holders {
		createPerm(t, ctx, store, id)
	}
	for _, id := range []string{"dnc-wb", "dnc-wq", "dnc-wp", "dnc-ws", "dnc-wk"} {
		createWisp(t, ctx, store, id)
	}
	addDependencyWithMeta(t, ctx, store, "dnc-b", "dnc-wb", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dnc-q", "dnc-wq", types.DepConditionalBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dnc-wp", "dnc-x", types.DepBlocks, "")
	addDependencyWithMeta(t, ctx, store, "dnc-c", "dnc-wp", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dnc-wk", "dnc-ws", types.DepParentChild, "")
	addDependencyWithMeta(t, ctx, store, "dnc-f", "dnc-ws", types.DepWaitsFor, "")
	for _, id := range []string{"dnc-wk", "dnc-wb", "dnc-wq", "dnc-wp", "dnc-ws"} {
		loseWisp(ctx, t, store, id)
	}

	for _, blocked := range []bool{true, false} {
		want := make(map[string]bool, len(holders))
		for _, id := range holders {
			plantIsBlocked(ctx, t, store, id, blocked)
			want[id] = blocked
		}
		commitAll(ctx, t, store, "merged flags")
		head, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("read HEAD: %v", err)
		}

		changed, err := store.RecomputeAllBlocked(ctx)
		if err != nil {
			t.Fatalf("flags %v: RecomputeAllBlocked: %v", blocked, err)
		}
		if changed != 0 {
			t.Errorf("flags %v: RecomputeAllBlocked corrected %d rows, want 0", blocked, changed)
		}
		after, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("read HEAD: %v", err)
		}
		if after != head {
			t.Errorf("flags %v: the repair minted commit %s over %s, want none", blocked, after, head)
		}
		assertBlockedFlags(ctx, t, store, "after the store repair", want)
	}
}
