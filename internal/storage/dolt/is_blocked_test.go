package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func getIsBlocked(t *testing.T, ctx context.Context, store *DoltStore, table, id string) bool {
	t.Helper()
	var b int
	//nolint:gosec // G201: table is a hardcoded "issues" or "wisps" from callers.
	err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", id).Scan(&b)
	if err != nil {
		t.Fatalf("read is_blocked from %s for %s: %v", table, id, err)
	}
	return b != 0
}

func TestIsBlocked_FreshIssueIsNotBlocked(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-fresh-1")

	if getIsBlocked(t, ctx, store, "issues", "isb-fresh-1") {
		t.Fatal("fresh issue should have is_blocked = 0")
	}
}

func TestIsBlocked_AddRemoveBlocksDepFlips(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-blocker")
	createPerm(t, ctx, store, "isb-blocked")

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-blocked", DependsOnID: "isb-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-blocked") {
		t.Fatal("expected is_blocked = 1 after adding blocks dep")
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-blocker") {
		t.Fatal("blocker itself should not be is_blocked")
	}

	if err := store.RemoveDependency(ctx, "isb-blocked", "isb-blocker", "tester"); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-blocked") {
		t.Fatal("expected is_blocked = 0 after removing blocks dep")
	}
}

func TestIsBlocked_CloseReopenBlockerFlipsDepender(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-cr-blocker")
	createPerm(t, ctx, store, "isb-cr-blocked")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-cr-blocked", DependsOnID: "isb-cr-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-cr-blocked") {
		t.Fatal("expected blocked after dep add")
	}

	if err := store.CloseIssue(ctx, "isb-cr-blocker", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-cr-blocked") {
		t.Fatal("expected unblocked after blocker closes")
	}

	if err := store.ReopenIssue(ctx, "isb-cr-blocker", "", "tester"); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-cr-blocked") {
		t.Fatal("expected blocked again after blocker reopens")
	}
}

func TestIsBlocked_PinStatusBehavesLikeClose(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-pin-blocker")
	createPerm(t, ctx, store, "isb-pin-blocked")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-pin-blocked", DependsOnID: "isb-pin-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-pin-blocked") {
		t.Fatal("expected blocked after dep add")
	}

	if err := store.UpdateIssue(ctx, "isb-pin-blocker", map[string]interface{}{
		"status": string(types.StatusPinned),
	}, "tester"); err != nil {
		t.Fatalf("pin blocker: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-pin-blocked") {
		t.Fatal("expected unblocked when blocker status is pinned")
	}

	if err := store.UpdateIssue(ctx, "isb-pin-blocker", map[string]interface{}{
		"status": string(types.StatusOpen),
	}, "tester"); err != nil {
		t.Fatalf("unpin blocker: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-pin-blocked") {
		t.Fatal("expected blocked again after blocker unpinned to open")
	}
}

func TestIsBlocked_ParentChildTransitivePropagation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-gp-blocker")
	createPerm(t, ctx, store, "isb-gp")
	createPerm(t, ctx, store, "isb-parent")
	createPerm(t, ctx, store, "isb-child")

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-parent", DependsOnID: "isb-gp", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("parent-child gp->parent: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-child", DependsOnID: "isb-parent", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("parent-child parent->child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-gp", DependsOnID: "isb-gp-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("blocks gp: %v", err)
	}

	for _, id := range []string{"isb-gp", "isb-parent", "isb-child"} {
		if !getIsBlocked(t, ctx, store, "issues", id) {
			t.Fatalf("%s should be is_blocked = 1 (transitive)", id)
		}
	}

	if err := store.CloseIssue(ctx, "isb-gp-blocker", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue gp-blocker: %v", err)
	}
	for _, id := range []string{"isb-gp", "isb-parent", "isb-child"} {
		if getIsBlocked(t, ctx, store, "issues", id) {
			t.Fatalf("%s should be is_blocked = 0 after gp-blocker closes", id)
		}
	}
}

func TestIsBlocked_CascadeDeleteClearsDepender(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-del-blocker")
	createPerm(t, ctx, store, "isb-del-blocked")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-del-blocked", DependsOnID: "isb-del-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-del-blocked") {
		t.Fatal("expected blocked before delete")
	}

	if err := store.DeleteIssue(ctx, "isb-del-blocker"); err != nil {
		t.Fatalf("DeleteIssue blocker: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-del-blocked") {
		t.Fatal("expected unblocked after blocker delete")
	}
}

func TestIsBlocked_BatchedCreateWithDepsInOneTxn(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-batch-blocker")

	parent := &types.Issue{
		ID: "isb-batch-parent", Title: "p", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{DependsOnID: "isb-batch-blocker", Type: types.DepBlocks},
		},
	}
	child := &types.Issue{
		ID: "isb-batch-child", Title: "c", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{DependsOnID: "isb-batch-parent", Type: types.DepParentChild},
		},
	}
	if err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{parent, child}, "tester",
		storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		}); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions: %v", err)
	}

	if !getIsBlocked(t, ctx, store, "issues", "isb-batch-parent") {
		t.Fatal("parent should be is_blocked = 1 (direct blocker)")
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-batch-child") {
		t.Fatal("child should be is_blocked = 1 (inherits from parent)")
	}
}

func TestIsBlocked_ConditionalBlocksAndWaitsFor(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-cb-target")
	createPerm(t, ctx, store, "isb-cb-source")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-cb-source", DependsOnID: "isb-cb-target", Type: types.DepConditionalBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency conditional-blocks: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-cb-source") {
		t.Fatal("expected is_blocked = 1 via conditional-blocks")
	}
}

func TestIsBlocked_WaitsForDefaultGate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-wf-default-waiter")
	createPerm(t, ctx, store, "isb-wf-default-spawner")
	createPerm(t, ctx, store, "isb-wf-default-child")

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-default-child", DependsOnID: "isb-wf-default-spawner", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("parent-child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-default-waiter", DependsOnID: "isb-wf-default-spawner", Type: types.DepWaitsFor,
	}, "tester"); err != nil {
		t.Fatalf("waits-for: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-default-waiter") {
		t.Fatal("expected waiter blocked: active child exists under default gate")
	}

	if err := store.CloseIssue(ctx, "isb-wf-default-child", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue child: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-wf-default-waiter") {
		t.Fatal("expected waiter unblocked: all children closed under default gate")
	}
}

func TestIsBlocked_WaitsForAnyChildrenGate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-wf-any-waiter")
	createPerm(t, ctx, store, "isb-wf-any-spawner")
	createPerm(t, ctx, store, "isb-wf-any-child-1")
	createPerm(t, ctx, store, "isb-wf-any-child-2")

	for _, child := range []string{"isb-wf-any-child-1", "isb-wf-any-child-2"} {
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: "isb-wf-any-spawner", Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("parent-child %s: %v", child, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-any-waiter", DependsOnID: "isb-wf-any-spawner",
		Type: types.DepWaitsFor, Metadata: `{"gate":"any-children"}`,
	}, "tester"); err != nil {
		t.Fatalf("waits-for any-children: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-any-waiter") {
		t.Fatal("expected waiter blocked: no children closed yet under any-children gate")
	}

	if err := store.CloseIssue(ctx, "isb-wf-any-child-1", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue first child: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-wf-any-waiter") {
		t.Fatal("expected waiter unblocked: any-children gate satisfied by one closed child")
	}

	if err := store.ReopenIssue(ctx, "isb-wf-any-child-1", "", "tester"); err != nil {
		t.Fatalf("ReopenIssue child: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-any-waiter") {
		t.Fatal("expected waiter re-blocked: any-children gate no longer satisfied after reopen")
	}
}

// TestIsBlocked_WaitsForAlsoBlocksSpawnerOpen covers the GH#3783/#3875
// also_blocks provenance flag: a waits-for edge collapsed from a redundant
// needs/depends_on blocks edge (cmd/bd/cook.go collectDependencies) must
// additionally block while the spawner itself is open, not only while it
// has an open parent-child child — closing the pre-fanout window where the
// waiter could become ready before the spawner ever produced (or finished)
// its fanout.
func TestIsBlocked_WaitsForAlsoBlocksSpawnerOpen(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-wf-ab-waiter")
	createPerm(t, ctx, store, "isb-wf-ab-spawner")

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-ab-waiter", DependsOnID: "isb-wf-ab-spawner",
		Type: types.DepWaitsFor, Metadata: `{"gate":"all-children","also_blocks":true}`,
	}, "tester"); err != nil {
		t.Fatalf("waits-for also_blocks: %v", err)
	}

	// Spawner open, zero children spawned yet: the waiter must be blocked.
	// Without also_blocks this would be unblocked (no children => no
	// children-open EXISTS) — exactly the pre-fanout regression.
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-ab-waiter") {
		t.Fatal("expected waiter blocked: spawner still open with zero children spawned (also_blocks)")
	}

	if err := store.CloseIssue(ctx, "isb-wf-ab-spawner", "no fanout needed", "tester", ""); err != nil {
		t.Fatalf("CloseIssue spawner: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-wf-ab-waiter") {
		t.Fatal("expected waiter unblocked after its childless spawner closed")
	}

	if err := store.ReopenIssue(ctx, "isb-wf-ab-spawner", "", "tester"); err != nil {
		t.Fatalf("ReopenIssue spawner: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-ab-waiter") {
		t.Fatal("expected waiter re-blocked after spawner reopened")
	}
}

// TestIsBlocked_WaitsForAlsoBlocksOverridesAnyChildrenEarlyOpen covers the
// override case explicitly called out in the design: also_blocks is a
// top-level OR outside the any-children early-open carve-out, so a
// collapsed edge on an any-children gate must stay blocked while the
// spawner itself is open even after one child closes (the normal
// any-children early-open behavior is preserved only for edges WITHOUT
// also_blocks — see TestIsBlocked_WaitsForAnyChildrenGate above).
func TestIsBlocked_WaitsForAlsoBlocksOverridesAnyChildrenEarlyOpen(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-wf-ab-any-waiter")
	createPerm(t, ctx, store, "isb-wf-ab-any-spawner")
	createPerm(t, ctx, store, "isb-wf-ab-any-child-1")
	createPerm(t, ctx, store, "isb-wf-ab-any-child-2")

	for _, child := range []string{"isb-wf-ab-any-child-1", "isb-wf-ab-any-child-2"} {
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: "isb-wf-ab-any-spawner", Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("parent-child %s: %v", child, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-ab-any-waiter", DependsOnID: "isb-wf-ab-any-spawner",
		Type: types.DepWaitsFor, Metadata: `{"gate":"any-children","also_blocks":true}`,
	}, "tester"); err != nil {
		t.Fatalf("waits-for any-children also_blocks: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-ab-any-waiter") {
		t.Fatal("expected waiter blocked: no children closed yet")
	}

	// Close one child: under a plain any-children gate this alone would
	// unblock (see TestIsBlocked_WaitsForAnyChildrenGate). With also_blocks
	// the waiter must STAY blocked because the spawner itself is still open.
	if err := store.CloseIssue(ctx, "isb-wf-ab-any-child-1", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue first child: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-ab-any-waiter") {
		t.Fatal("expected waiter STILL blocked: also_blocks overrides the any-children early-open carve-out while the spawner is open")
	}

	// Now close the spawner too: the gate is satisfied (a child closed under
	// any-children) AND the spawner itself is closed, so the waiter unblocks.
	if err := store.CloseIssue(ctx, "isb-wf-ab-any-spawner", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue spawner: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-wf-ab-any-waiter") {
		t.Fatal("expected waiter unblocked once the spawner closes with the any-children gate already satisfied")
	}
}

func TestIsBlocked_AddClosedChildUnblocksAnyChildrenGate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-wf-any-add-waiter")
	createPerm(t, ctx, store, "isb-wf-any-add-spawner")
	createPerm(t, ctx, store, "isb-wf-any-add-active-child")
	createPerm(t, ctx, store, "isb-wf-any-add-closed-child")

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-any-add-active-child", DependsOnID: "isb-wf-any-add-spawner", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("parent-child active: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-any-add-waiter", DependsOnID: "isb-wf-any-add-spawner",
		Type: types.DepWaitsFor, Metadata: `{"gate":"any-children"}`,
	}, "tester"); err != nil {
		t.Fatalf("waits-for any-children: %v", err)
	}
	if !getIsBlocked(t, ctx, store, "issues", "isb-wf-any-add-waiter") {
		t.Fatal("expected waiter blocked before any child is closed")
	}

	if err := store.CloseIssue(ctx, "isb-wf-any-add-closed-child", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue closed child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-wf-any-add-closed-child", DependsOnID: "isb-wf-any-add-spawner", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("parent-child closed: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-wf-any-add-waiter") {
		t.Fatal("expected waiter unblocked after linking an already-closed child")
	}
}

func TestIsBlocked_ClosedDependerNotRemarkedActive(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "isb-cd-blocker")
	createPerm(t, ctx, store, "isb-cd-depender")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-cd-depender", DependsOnID: "isb-cd-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := store.CloseIssue(ctx, "isb-cd-depender", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue depender: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-cd-depender") {
		t.Fatal("closed depender should be is_blocked = 0")
	}
}

// TestIsBlocked_ConditionalBlocksHoldsOnSuccessClose pins conditional-blocks'
// documented rule, B runs only if A fails: closing the precondition releases
// the fallback (stored bit 0, in ready work) exactly when the close reason is
// a failure (types.IsFailureClose); any other reason, the default "Closed" and
// the empty reason included, keeps it blocked.
func TestIsBlocked_ConditionalBlocksHoldsOnSuccessClose(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	cases := []struct {
		name, reason string
	}{
		{"success", "completed successfully"},
		{"default", "Closed"},
		{"empty", ""},
		{"failure", "failed: upstream returned 500"},
		{"wontfix", "Won't fix"},
	}
	for i, tc := range cases {
		a := "isb-cbs-a" + string(rune('0'+i))
		b := "isb-cbs-b" + string(rune('0'+i))
		createPerm(t, ctx, store, a)
		createPerm(t, ctx, store, b)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: b, DependsOnID: a, Type: types.DepConditionalBlocks,
		}, "tester"); err != nil {
			t.Fatalf("%s: AddDependency: %v", tc.name, err)
		}
		if err := store.CloseIssue(ctx, a, tc.reason, "tester", ""); err != nil {
			t.Fatalf("%s: CloseIssue: %v", tc.name, err)
		}

		wantBlocked := !types.IsFailureClose(tc.reason)
		if got := getIsBlocked(t, ctx, store, "issues", b); got != wantBlocked {
			t.Errorf("%s: close reason %q: stored is_blocked(%s) = %v, want %v", tc.name, tc.reason, b, got, wantBlocked)
		}
		ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen})
		if err != nil {
			t.Fatalf("%s: GetReadyWork: %v", tc.name, err)
		}
		inReady := false
		for _, issue := range ready {
			if issue.ID == b {
				inReady = true
			}
		}
		if inReady == wantBlocked {
			t.Errorf("%s: close reason %q: %s in ready work = %v, want %v", tc.name, tc.reason, b, inReady, !wantBlocked)
		}
	}

	// Compatibility control: an ordinary blocks edge releases on any close.
	createPerm(t, ctx, store, "isb-cbs-plain-a")
	createPerm(t, ctx, store, "isb-cbs-plain-b")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "isb-cbs-plain-b", DependsOnID: "isb-cbs-plain-a", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency blocks: %v", err)
	}
	if err := store.CloseIssue(ctx, "isb-cbs-plain-a", "completed successfully", "tester", ""); err != nil {
		t.Fatalf("CloseIssue plain: %v", err)
	}
	if getIsBlocked(t, ctx, store, "issues", "isb-cbs-plain-b") {
		t.Error("ordinary blocks dependent should be released by a success close")
	}
}

// TestIsBlocked_ConditionalBlocksWispRoutes runs the conditional-blocks close
// rule through every source/target combination that contains a wisp: the
// fallback B is released (stored bit 0, in ready work) only when A closes with
// a failure reason.
func TestIsBlocked_ConditionalBlocksWispRoutes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	type route struct {
		name               string
		sourceWisp, tgtWsp bool
	}
	routes := []route{
		{"issue-to-wisp", false, true},
		{"wisp-to-issue", true, false},
		{"wisp-to-wisp", true, true},
	}
	reasons := []string{"completed successfully", "aborted by operator"}
	for ri, r := range routes {
		for oi, reason := range reasons {
			a := "isb-cbw-a" + string(rune('0'+ri)) + string(rune('0'+oi))
			b := "isb-cbw-b" + string(rune('0'+ri)) + string(rune('0'+oi))
			if r.tgtWsp {
				createWisp(t, ctx, store, a)
			} else {
				createPerm(t, ctx, store, a)
			}
			table := "issues"
			if r.sourceWisp {
				createWisp(t, ctx, store, b)
				table = "wisps"
			} else {
				createPerm(t, ctx, store, b)
			}
			if err := store.AddDependency(ctx, &types.Dependency{
				IssueID: b, DependsOnID: a, Type: types.DepConditionalBlocks,
			}, "tester"); err != nil {
				t.Fatalf("%s: AddDependency: %v", r.name, err)
			}
			if !getIsBlocked(t, ctx, store, table, b) {
				t.Fatalf("%s: %s should be blocked while %s is open", r.name, b, a)
			}
			if err := store.CloseIssue(ctx, a, reason, "tester", ""); err != nil {
				t.Fatalf("%s: CloseIssue: %v", r.name, err)
			}

			wantBlocked := !types.IsFailureClose(reason)
			if got := getIsBlocked(t, ctx, store, table, b); got != wantBlocked {
				t.Errorf("%s: close reason %q: stored is_blocked(%s) = %v, want %v", r.name, reason, b, got, wantBlocked)
			}
			ready, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen, IncludeEphemeral: true})
			if err != nil {
				t.Fatalf("%s: GetReadyWork: %v", r.name, err)
			}
			inReady := false
			for _, issue := range ready {
				if issue.ID == b {
					inReady = true
				}
			}
			if inReady == wantBlocked {
				t.Errorf("%s: close reason %q: %s in ready work = %v, want %v", r.name, reason, b, inReady, !wantBlocked)
			}
		}
	}
}

// TestIsBlocked_ConditionalBlocksInsertOntoClosedTarget inserts a
// conditional-blocks edge onto an already-closed precondition through the
// store's direct-insertion writer, on every issue/wisp route: the source is
// blocked exactly when the target's close reason is not a failure.
func TestIsBlocked_ConditionalBlocksInsertOntoClosedTarget(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	routes := []struct {
		name                   string
		sourceWisp, targetWisp bool
	}{
		{"issue-to-issue", false, false},
		{"issue-to-wisp", false, true},
		{"wisp-to-issue", true, false},
		{"wisp-to-wisp", true, true},
	}
	reasons := []string{"completed successfully", "rejected by review"}
	for ri, r := range routes {
		for oi, reason := range reasons {
			suffix := string(rune('0'+ri)) + string(rune('0'+oi))
			a, b := "isb-cbi-a"+suffix, "isb-cbi-b"+suffix
			if r.targetWisp {
				createWisp(t, ctx, store, a)
			} else {
				createPerm(t, ctx, store, a)
			}
			if err := store.CloseIssue(ctx, a, reason, "tester", ""); err != nil {
				t.Fatalf("%s: CloseIssue: %v", r.name, err)
			}
			table := "issues"
			if r.sourceWisp {
				createWisp(t, ctx, store, b)
				table = "wisps"
			} else {
				createPerm(t, ctx, store, b)
			}
			if err := store.AddDependency(ctx, &types.Dependency{
				IssueID: b, DependsOnID: a, Type: types.DepConditionalBlocks,
			}, "tester"); err != nil {
				t.Fatalf("%s: AddDependency: %v", r.name, err)
			}
			want := !types.IsFailureClose(reason)
			if got := getIsBlocked(t, ctx, store, table, b); got != want {
				t.Errorf("%s: insert onto target closed %q: stored is_blocked(%s) = %v, want %v", r.name, reason, b, got, want)
			}
		}
	}

	// Cross-session precheck: inside one transaction, an issue source and a
	// wisp target live on different sessions, so the writer gates on the
	// target row read through readDepTargetForPrecheck.
	for oi, reason := range reasons {
		suffix := string(rune('0' + oi))
		a, b := "isb-cbi-tx-a"+suffix, "isb-cbi-tx-b"+suffix
		if err := store.RunInTransaction(ctx, "conditional insert "+suffix, func(tx storage.Transaction) error {
			if err := tx.CreateIssue(ctx, &types.Issue{ID: a, Title: "wisp " + a, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "tester"); err != nil {
				return err
			}
			if err := tx.CloseIssue(ctx, a, reason, "tester", ""); err != nil {
				return err
			}
			if err := tx.CreateIssue(ctx, &types.Issue{ID: b, Title: "perm " + b, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
				return err
			}
			return tx.AddDependency(ctx, &types.Dependency{IssueID: b, DependsOnID: a, Type: types.DepConditionalBlocks}, "tester")
		}); err != nil {
			t.Fatalf("tx route %q: %v", reason, err)
		}
		want := !types.IsFailureClose(reason)
		if got := getIsBlocked(t, ctx, store, "issues", b); got != want {
			t.Errorf("tx issue-to-wisp: insert onto target closed %q: stored is_blocked(%s) = %v, want %v", reason, b, got, want)
		}
	}
}

// TestIsBlocked_ConditionalBlocksReasonEditFlipsDependent edits only a closed
// target's close reason through UpdateIssue: crossing between a non-failure
// and a failure reason flips the conditional dependent's stored bit in the
// same write, without touching the dependent's updated_at.
func TestIsBlocked_ConditionalBlocksReasonEditFlipsDependent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	updatedAt := func(id string) string {
		t.Helper()
		var s string
		if err := store.db.QueryRowContext(ctx, "SELECT CAST(updated_at AS CHAR) FROM issues WHERE id = ?", id).Scan(&s); err != nil {
			t.Fatalf("read updated_at for %s: %v", id, err)
		}
		return s
	}

	for i, targetWisp := range []bool{false, true} {
		suffix := string(rune('0' + i))
		a, b := "isb-cbr-a"+suffix, "isb-cbr-b"+suffix
		if targetWisp {
			createWisp(t, ctx, store, a)
		} else {
			createPerm(t, ctx, store, a)
		}
		createPerm(t, ctx, store, b)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: b, DependsOnID: a, Type: types.DepConditionalBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if err := store.CloseIssue(ctx, a, "completed successfully", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		if !getIsBlocked(t, ctx, store, "issues", b) {
			t.Fatalf("target wisp=%v: success close should leave %s blocked", targetWisp, b)
		}
		before := updatedAt(b)

		steps := []struct {
			reason      string
			wantBlocked bool
		}{
			{"failed downstream", false},
			{"done after all", true},
		}
		for _, step := range steps {
			if err := store.UpdateIssue(ctx, a, map[string]interface{}{"close_reason": step.reason}, "tester"); err != nil {
				t.Fatalf("UpdateIssue close_reason %q: %v", step.reason, err)
			}
			if got := getIsBlocked(t, ctx, store, "issues", b); got != step.wantBlocked {
				t.Errorf("target wisp=%v: reason edited to %q: stored is_blocked(%s) = %v, want %v", targetWisp, step.reason, b, got, step.wantBlocked)
			}
			if after := updatedAt(b); after != before {
				t.Errorf("target wisp=%v: reason edit to %q touched %s updated_at: %s -> %s", targetWisp, step.reason, b, before, after)
			}
		}
	}
}

// TestIsBlocked_ConditionalBlocksSuccessHoldIsListedButClosable pins the
// read surfaces for a dependent held only by a success-closed conditional
// target: the blocked listing names the target as the blocker, the blocked
// reads report blocked with no live blockers (the target is closed, so it
// cannot be worked), and a manual close without force goes through.
func TestIsBlocked_ConditionalBlocksSuccessHoldIsListedButClosable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	a, b := "isb-cbl-a", "isb-cbl-b"
	createPerm(t, ctx, store, a)
	createPerm(t, ctx, store, b)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: b, DependsOnID: a, Type: types.DepConditionalBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := store.CloseIssue(ctx, a, "completed successfully", "tester", ""); err != nil {
		t.Fatalf("CloseIssue A: %v", err)
	}

	listed, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	var entry *types.BlockedIssue
	for _, item := range listed {
		if item.ID == b {
			entry = item
		}
	}
	if entry == nil {
		t.Fatalf("blocked listing omits %s held by success-closed conditional %s", b, a)
	}
	if len(entry.BlockedBy) != 1 || entry.BlockedBy[0] != a || entry.BlockedByCount != 1 {
		t.Errorf("blocked listing names %v (count %d) as %s's blockers, want [%s]", entry.BlockedBy, entry.BlockedByCount, b, a)
	}

	blocked, blockers, err := store.IsBlocked(ctx, b)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked || len(blockers) != 0 {
		t.Errorf("IsBlocked(%s) = %v, %v; want true with no live blockers", b, blocked, blockers)
	}
	batch, err := store.IsBlockedBatch(ctx, []string{b})
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	if !batch[b] {
		t.Errorf("IsBlockedBatch(%s) = false, want true", b)
	}
	withBlockers, err := store.IsBlockedWithBlockersBatch(ctx, []string{b})
	if err != nil {
		t.Fatalf("IsBlockedWithBlockersBatch: %v", err)
	}
	if got, ok := withBlockers[b]; !ok || len(got) != 0 {
		t.Errorf("IsBlockedWithBlockersBatch(%s) = %v (present %v), want present with no live blockers", b, got, ok)
	}

	if _, err := store.CloseIssueChecked(ctx, b, "tester", storage.CloseIssueOptions{Reason: "done by hand"}); err != nil {
		t.Fatalf("manual close of %s without force: %v", b, err)
	}
}
