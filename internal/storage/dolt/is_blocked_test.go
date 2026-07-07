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
			OrphanHandling:       storage.OrphanAllow,
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
