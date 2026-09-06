package dolt

// Differential coverage for the full is_blocked repair (bd-t9ypt): the
// full-repair SQL must agree with the scoped per-write templates on EVERY
// leg of the should-be-blocked disjunction — issue blockers, wisp blockers,
// issue parents, wisp parents, and the waits-for gate in all its metadata
// modes (default all-children, any-children, also_blocks,
// also_blocks-over-any-children) — on both the issues table and the wisps
// table.
//
// Ground truth is produced by the scoped write path (blocked_state.go
// batched templates); then every is_blocked flag in both tables is inverted
// and the full repair must restore the exact truth, after which detection
// must count 0 and a second repair must be a no-op. The existing lockstep
// tests in blocked_consistency_test.go pin legs 1 and 3 (issue blocker,
// issue parent) with exact corrected-row counts; this test pins the
// remaining legs, which would otherwise have no full-repair coverage.

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func readIsBlockedFlags(t *testing.T, ctx context.Context, store *DoltStore, table string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	//nolint:gosec // G201: table is a hardcoded "issues" or "wisps" from callers.
	rows, err := store.db.QueryContext(ctx, "SELECT id, is_blocked FROM "+table)
	if err != nil {
		t.Fatalf("read %s flags: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b int
		if err := rows.Scan(&id, &b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = b != 0
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func addDependencyWithMeta(t *testing.T, ctx context.Context, store *DoltStore, source, target string, depType types.DependencyType, metadata string) {
	t.Helper()
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: source, DependsOnID: target, Type: depType, Metadata: metadata,
	}, "tester"); err != nil {
		t.Fatalf("add dep %s -> %s (%s): %v", source, target, depType, err)
	}
}

func TestRecomputeAllIsBlocked_FullRepairAllLegsDifferential(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	dep := func(src, tgt string, ty types.DependencyType) { addDependencyWithMeta(t, ctx, store, src, tgt, ty, "") }
	closeIssue := func(id string) {
		t.Helper()
		if err := store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
			t.Fatalf("close %s: %v", id, err)
		}
	}

	// ---- Leg 1: issue blocked by issue (blocks / conditional-blocks) ----
	createPerm(t, ctx, store, "zz-b1")  // open blocker
	createPerm(t, ctx, store, "zz-b2")  // will be closed
	createPerm(t, ctx, store, "zz-b3")  // will be pinned
	createPerm(t, ctx, store, "zz-a1")  // blocked by zz-b1
	createPerm(t, ctx, store, "zz-a1c") // conditional-blocks on zz-b1
	createPerm(t, ctx, store, "zz-a2")  // dep on closed blocker => unblocked
	createPerm(t, ctx, store, "zz-a3")  // dep on pinned blocker => unblocked
	dep("zz-a1", "zz-b1", types.DepBlocks)
	dep("zz-a1c", "zz-b1", types.DepConditionalBlocks)
	dep("zz-a2", "zz-b2", types.DepBlocks)
	dep("zz-a3", "zz-b3", types.DepBlocks)
	closeIssue("zz-b2")
	if err := store.UpdateIssue(ctx, "zz-b3", map[string]interface{}{"status": string(types.StatusPinned)}, "tester"); err != nil {
		t.Fatalf("pin zz-b3: %v", err)
	}

	// ---- Leg 2: issue blocked by open WISP ----
	createWisp(t, ctx, store, "zz-w1") // open wisp blocker
	createWisp(t, ctx, store, "zz-w2") // closed wisp blocker
	createPerm(t, ctx, store, "zz-a4") // blocked by open wisp
	createPerm(t, ctx, store, "zz-a5") // dep on closed wisp => unblocked
	dep("zz-a4", "zz-w1", types.DepBlocks)
	dep("zz-a5", "zz-w2", types.DepBlocks)
	closeIssue("zz-w2")

	// ---- Leg 3: issue child of blocked issue parent, plus grandchild cascade ----
	createPerm(t, ctx, store, "zz-c1") // child of blocked zz-a1
	createPerm(t, ctx, store, "zz-c2") // grandchild
	dep("zz-c1", "zz-a1", types.DepParentChild)
	dep("zz-c2", "zz-c1", types.DepParentChild)

	// ---- Leg 4: issue child of blocked WISP parent ----
	createWisp(t, ctx, store, "zz-wp") // wisp blocked by zz-b1
	dep("zz-wp", "zz-b1", types.DepBlocks)
	createPerm(t, ctx, store, "zz-c3") // child of blocked wisp
	dep("zz-c3", "zz-wp", types.DepParentChild)

	// ---- Leg 5: waits-for gates on issues ----
	// default all-children gate, open child => blocked
	createPerm(t, ctx, store, "zz-s1")
	createPerm(t, ctx, store, "zz-sc1")
	createPerm(t, ctx, store, "zz-d1")
	dep("zz-sc1", "zz-s1", types.DepParentChild)
	dep("zz-d1", "zz-s1", types.DepWaitsFor)
	// any-children gate satisfied by one closed child => unblocked
	createPerm(t, ctx, store, "zz-s2")
	createPerm(t, ctx, store, "zz-sc2")
	createPerm(t, ctx, store, "zz-sc3")
	createPerm(t, ctx, store, "zz-d2")
	dep("zz-sc2", "zz-s2", types.DepParentChild)
	dep("zz-sc3", "zz-s2", types.DepParentChild)
	addDependencyWithMeta(t, ctx, store, "zz-d2", "zz-s2", types.DepWaitsFor, `{"gate":"any-children"}`)
	closeIssue("zz-sc2")
	// also_blocks: spawner open, zero children => blocked
	createPerm(t, ctx, store, "zz-s3")
	createPerm(t, ctx, store, "zz-d3")
	addDependencyWithMeta(t, ctx, store, "zz-d3", "zz-s3", types.DepWaitsFor, `{"gate":"all-children","also_blocks":true}`)
	// also_blocks overrides any-children early-open: child closed, spawner open => blocked
	createPerm(t, ctx, store, "zz-s4")
	createPerm(t, ctx, store, "zz-sc4")
	createPerm(t, ctx, store, "zz-d4")
	dep("zz-sc4", "zz-s4", types.DepParentChild)
	addDependencyWithMeta(t, ctx, store, "zz-d4", "zz-s4", types.DepWaitsFor, `{"gate":"any-children","also_blocks":true}`)
	closeIssue("zz-sc4")

	// ---- Wisp-table side (wisp_dependencies) ----
	createWisp(t, ctx, store, "zz-wa") // wisp blocked by open issue
	dep("zz-wa", "zz-b1", types.DepBlocks)
	createWisp(t, ctx, store, "zz-wb") // wisp child of blocked wisp parent
	dep("zz-wb", "zz-wp", types.DepParentChild)
	createWisp(t, ctx, store, "zz-wneg") // wisp dep on closed issue => unblocked
	dep("zz-wneg", "zz-b2", types.DepBlocks)
	// wisp waits-for a wisp spawner with an open wisp child => blocked
	createWisp(t, ctx, store, "zz-ws")
	createWisp(t, ctx, store, "zz-wsc")
	createWisp(t, ctx, store, "zz-wd")
	dep("zz-wsc", "zz-ws", types.DepParentChild)
	dep("zz-wd", "zz-ws", types.DepWaitsFor)

	// ---- ground truth from the scoped write path ----
	truthIssues := readIsBlockedFlags(t, ctx, store, "issues")
	truthWisps := readIsBlockedFlags(t, ctx, store, "wisps")

	// Sanity-pin the interesting expectations so the fixture itself is not vacuous.
	expectBlockedIssues := []string{"zz-a1", "zz-a1c", "zz-a4", "zz-c1", "zz-c2", "zz-c3", "zz-d1", "zz-d3", "zz-d4"}
	expectUnblockedIssues := []string{"zz-a2", "zz-a3", "zz-a5", "zz-d2", "zz-b1", "zz-s1", "zz-s2", "zz-s3", "zz-s4"}
	for _, id := range expectBlockedIssues {
		if !truthIssues[id] {
			t.Fatalf("fixture: expected %s blocked by write path", id)
		}
	}
	for _, id := range expectUnblockedIssues {
		if truthIssues[id] {
			t.Fatalf("fixture: expected %s unblocked by write path", id)
		}
	}
	for _, id := range []string{"zz-wp", "zz-wa", "zz-wb", "zz-wd"} {
		if !truthWisps[id] {
			t.Fatalf("fixture: expected wisp %s blocked by write path", id)
		}
	}
	for _, id := range []string{"zz-w1", "zz-wneg", "zz-ws", "zz-wsc"} {
		if truthWisps[id] {
			t.Fatalf("fixture: expected wisp %s unblocked by write path", id)
		}
	}

	// Consistent plane: detection counts 0.
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, store.db); err != nil || n != 0 {
		t.Fatalf("pre-corruption count: want 0/nil, got %d/%v", n, err)
	}

	// ---- corrupt: invert EVERY flag in both tables ----
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 - is_blocked"); err != nil {
		t.Fatalf("invert issues: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE wisps SET is_blocked = 1 - is_blocked"); err != nil {
		t.Fatalf("invert wisps: %v", err)
	}
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, store.db); err != nil || n == 0 {
		t.Fatalf("post-corruption count: want >0/nil, got %d/%v", n, err)
	}

	// ---- full repair ----
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeAllIsBlockedInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if changed == 0 {
		t.Fatal("repair reported 0 corrections over a fully inverted plane")
	}

	// ---- compare against ground truth ----
	gotIssues := readIsBlockedFlags(t, ctx, store, "issues")
	gotWisps := readIsBlockedFlags(t, ctx, store, "wisps")
	for id, want := range truthIssues {
		if gotIssues[id] != want {
			t.Errorf("issues.%s: repair produced is_blocked=%v, write-path truth %v", id, gotIssues[id], want)
		}
	}
	for id, want := range truthWisps {
		if gotWisps[id] != want {
			t.Errorf("wisps.%s: repair produced is_blocked=%v, write-path truth %v", id, gotWisps[id], want)
		}
	}

	// Detection agrees and the repair is idempotent.
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, store.db); err != nil || n != 0 {
		t.Fatalf("post-repair count: want 0/nil, got %d/%v", n, err)
	}
	tx2, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin2: %v", err)
	}
	again, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx2)
	if err != nil {
		_ = tx2.Rollback()
		t.Fatalf("second repair: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit2: %v", err)
	}
	if again != 0 {
		t.Fatalf("second repair must be a no-op, corrected %d", again)
	}
}
