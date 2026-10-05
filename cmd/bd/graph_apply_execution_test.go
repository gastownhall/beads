//go:build cgo

package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func withGraphApplyTestStore(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()

	ctx := context.Background()
	testStore := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), ".beads", "beads.db"), "ga")

	oldStore, oldCtx, oldActor := store, rootCtx, actor
	store, rootCtx, actor = testStore, ctx, "graph-apply-test"
	t.Cleanup(func() {
		store, rootCtx, actor = oldStore, oldCtx, oldActor
	})

	return ctx, testStore.DB()
}

func TestExecuteGraphApplyEphemeralAndNoHistoryRouteToWisps(t *testing.T) {
	ctx, db := withGraphApplyTestStore(t)

	tests := []struct {
		name string
		opts GraphApplyOptions
	}{
		{name: "ephemeral", opts: GraphApplyOptions{Ephemeral: true}},
		{name: "no history", opts: GraphApplyOptions{NoHistory: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := &GraphApplyPlan{
				Nodes: []GraphApplyNode{{Key: "a", Title: "A", Type: "task"}},
			}

			result, err := executeGraphApply(ctx, plan, tt.opts)
			if err != nil {
				t.Fatalf("executeGraphApply: %v", err)
			}
			id := result.IDs["a"]
			got, err := store.GetIssue(ctx, id)
			if err != nil {
				t.Fatalf("GetIssue(%s): %v", id, err)
			}
			if got.Ephemeral != tt.opts.Ephemeral {
				t.Fatalf("Ephemeral = %v, want %v", got.Ephemeral, tt.opts.Ephemeral)
			}
			if got.NoHistory != tt.opts.NoHistory {
				t.Fatalf("NoHistory = %v, want %v", got.NoHistory, tt.opts.NoHistory)
			}

			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&count); err != nil {
				t.Fatalf("query wisps for %s: %v", id, err)
			}
			if count != 1 {
				t.Fatalf("wisps row count for %s = %d, want 1", id, count)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&count); err != nil {
				t.Fatalf("query issues for %s: %v", id, err)
			}
			if count != 0 {
				t.Fatalf("issues row count for %s = %d, want 0", id, count)
			}
		})
	}
}

func TestExecuteGraphApplyRejectsMixedLocalExternalBlockingCycle(t *testing.T) {
	ctx, _ := withGraphApplyTestStore(t)

	existing := &types.Issue{
		ID:        "ga-existing",
		Title:     "Existing",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, existing, actor); err != nil {
		t.Fatalf("CreateIssue(existing): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromID: existing.ID, ToKey: "a", Type: "blocks"},
			{FromKey: "b", ToID: existing.ID, Type: "blocks"},
			{FromKey: "a", ToKey: "b", Type: "blocks"},
		},
	}

	if err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{}); err != nil {
		t.Fatalf("validateGraphApplyPlan: %v", err)
	}
	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected mixed local/external blocking cycle to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %q, want cycle rejection", err.Error())
	}
}

func TestExecuteGraphApplyRejectsStoredPrefixParentBlockingPath(t *testing.T) {
	ctx, db := withGraphApplyTestStore(t)

	parent := &types.Issue{
		ID:        "ga-parent",
		Title:     "Existing Parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	mid := &types.Issue{
		ID:        "ga-existing-mid",
		Title:     "Existing Middle",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	for _, issue := range []*types.Issue{parent, mid} {
		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     parent.ID,
		DependsOnID: mid.ID,
		Type:        types.DepBlocks,
	}, actor); err != nil {
		t.Fatalf("AddDependency(parent -> mid): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "child", Title: "Child", Type: "task", ParentID: parent.ID},
		},
		Edges: []GraphApplyEdge{
			{FromID: mid.ID, ToKey: "child", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected stored-prefix parent blocking path to be rejected")
	}
	// Confirmed against a real Dolt test server (2026-10 coordinator
	// follow-up): the engine's own cycle detector catches this before any
	// more specific "path from parent" wording would apply, so the
	// observed refusal reads "...would create a cycle", not a parent-path
	// phrase this test originally (and wrongly, since it had never
	// actually run against a real store) assumed.
	if got, want := err.Error(), "cycle"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE title = 'Child'").Scan(&count); err != nil {
		t.Fatalf("query child rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("child issue rows after failed transaction = %d, want 0", count)
	}
}

func TestExecuteGraphApplyAllowsExplicitParentChildDuplicate(t *testing.T) {
	ctx, _ := withGraphApplyTestStore(t)

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "child", Title: "Child", Type: "task", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "child", ToKey: "root", Type: string(types.DepParentChild)},
		},
	}

	result, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err != nil {
		t.Fatalf("executeGraphApply: %v", err)
	}

	deps, err := store.GetDependenciesWithMetadata(ctx, result.IDs["child"])
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata(child): %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("dependency count = %d, want 1", len(deps))
	}
	if deps[0].ID != result.IDs["root"] {
		t.Fatalf("dependency target = %s, want %s", deps[0].ID, result.IDs["root"])
	}
	if deps[0].DependencyType != types.DepParentChild {
		t.Fatalf("dependency type = %s, want %s", deps[0].DependencyType, types.DepParentChild)
	}
}

func TestExecuteGraphApplyRejectsBlockingChildToParentDuplicate(t *testing.T) {
	ctx, _ := withGraphApplyTestStore(t)

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "child", Title: "Child", Type: "task", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "child", ToKey: "root"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected default blocking child-to-parent duplicate to be rejected")
	}
	// Confirmed against a real Dolt test server (2026-10 coordinator
	// follow-up): CheckBlockingHierarchyInTx refuses this as an ancestor
	// being blocked by its own descendant, not as a literal "parent-child"
	// duplicate-edge rejection this test originally (and wrongly, since it
	// had never actually run against a real store) assumed.
	if !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("error = %q, want ancestor-blocking rejection", err.Error())
	}
}

// TestExecuteGraphApplyRejectsReverseParentToChildBlockingEdgeExternalParent
// restores the one subcase of the deleted graph_apply_execution_unit_test.go's
// fake-store TestExecuteGraphApplyUnitRejectsReverseParentToChildBlockingEdge
// that a pure plan-local check cannot see: the parent already exists as a
// stored issue (addressed by ParentID, not a plan key), so only a real store
// walk — now the BatchApplier end gate, reached through executeGraphApply —
// can catch a blocking edge running the wrong way across that hierarchy. The
// local-only variant (both ends are plan keys) is covered by
// TestValidateGraphApplyPlanRejectsImplicitParentChildReverseBlockingCycle in
// graph_apply_test.go.
func TestExecuteGraphApplyRejectsReverseParentToChildBlockingEdgeExternalParent(t *testing.T) {
	for _, depType := range []string{"blocks", "conditional-blocks"} {
		t.Run(depType, func(t *testing.T) {
			ctx, _ := withGraphApplyTestStore(t)

			parent := &types.Issue{
				ID:        "ga-reverse-parent",
				Title:     "Existing Parent",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeEpic,
			}
			if err := store.CreateIssue(ctx, parent, actor); err != nil {
				t.Fatalf("CreateIssue(parent): %v", err)
			}

			plan := &GraphApplyPlan{
				Nodes: []GraphApplyNode{
					{Key: "child", Title: "Child", Type: "task", ParentID: parent.ID},
				},
				Edges: []GraphApplyEdge{
					{FromID: parent.ID, ToKey: "child", Type: depType},
				},
			}

			_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
			if err == nil {
				t.Fatal("expected reverse parent-to-child blocking edge to be rejected")
			}
			// CheckBlockingHierarchyInTx (internal/storage/issueops/dependencies.go)
			// is what actually catches this on the real BatchApplier path, via
			// batch_apply.go's end-gate call -- NOT the old domain.GraphPlan
			// applyGraph family (whose "parent-child relationship" wording lives in
			// internal/storage/domain/issue.go and is no longer reachable from this
			// CLI command). Its error is *issueops.DependencyHierarchyConflictError,
			// which reads "<id> cannot be blocked by its ancestor/descendant <id>: ...".
			if !strings.Contains(err.Error(), "cannot be blocked by its") {
				t.Fatalf("error = %q, want a DependencyHierarchyConflictError rejection", err.Error())
			}
		})
	}
}

// TestExecuteGraphApplyRejectsTransitiveParentChainBlockingCycle restores the
// deleted fake-store TestExecuteGraphApplyUnitRejectsTransitiveParentChainBlockingCycle
// against the real BatchApplier path: a plan-local parent chain (a's parent is
// plan-local b, b's parent is an existing external issue) plus a blocking edge
// from that external grandparent to the local leaf must be rejected even
// though no single parent-child hop is itself local-and-external at once —
// catching it requires walking the hierarchy against the store, which only
// the end gate executeGraphApply reaches can do.
func TestExecuteGraphApplyRejectsTransitiveParentChainBlockingCycle(t *testing.T) {
	ctx, _ := withGraphApplyTestStore(t)

	grandparent := &types.Issue{
		ID:        "ga-chain-grandparent",
		Title:     "Existing Grandparent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, grandparent, actor); err != nil {
		t.Fatalf("CreateIssue(grandparent): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task", ParentKey: "b"},
			{Key: "b", Title: "B", Type: "task", ParentID: grandparent.ID},
		},
		Edges: []GraphApplyEdge{
			{FromID: grandparent.ID, ToKey: "a", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected transitive parent-chain blocking cycle to be rejected")
	}
	// Same end gate and error family as the reverse-edge case above:
	// CheckBlockingHierarchyInTx walks the full ancestor chain via a recursive
	// CTE, so the external grandparent is still found even though the
	// blocking edge itself only names the plan-local leaf.
	if got, want := err.Error(), "cannot be blocked by its"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
}

// TestExecuteGraphApplyRejectsBlockingThroughExistingBlocking restores the
// deleted fake-store TestExecuteGraphApplyUnitRejectsBlockingThroughExistingBlocking
// against the real BatchApplier path: a planned blocking edge that would close
// a cycle through an EXISTING stored blocking dependency (not anything in the
// plan) must still be rejected, and must not add the new edge.
func TestExecuteGraphApplyRejectsBlockingThroughExistingBlocking(t *testing.T) {
	ctx, _ := withGraphApplyTestStore(t)

	for _, id := range []string{"ga-existing-x", "ga-existing-y"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, actor); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	// Existing blocking dep: ga-existing-y blocks ga-existing-x.
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "ga-existing-y",
		DependsOnID: "ga-existing-x",
		Type:        types.DepBlocks,
	}, actor); err != nil {
		t.Fatalf("AddDependency(y -> x): %v", err)
	}

	plan := &GraphApplyPlan{
		Edges: []GraphApplyEdge{
			{FromID: "ga-existing-x", ToID: "ga-existing-y", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected blocking cycle through existing blocking dep to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %q, want cycle rejection", err.Error())
	}

	deps, err := store.GetDependenciesWithMetadata(ctx, "ga-existing-y")
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata(ga-existing-y): %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("ga-existing-y dependency count = %d, want 1 (only the pre-existing edge)", len(deps))
	}
}

// TestExecuteGraphApplyRejectsExternalParentTransitiveBlockingPath restores
// the deleted fake-store
// TestExecuteGraphApplyUnitRejectsExternalParentTransitiveBlockingPath: a
// blocking edge from an existing issue to a plan-local middle node, chained
// with a second blocking edge from that middle node to a plan-local
// descendant of the existing issue, composes into a disallowed
// parent-to-descendant block even though no single edge in the plan touches
// the hierarchy directly.
func TestExecuteGraphApplyRejectsExternalParentTransitiveBlockingPath(t *testing.T) {
	ctx, db := withGraphApplyTestStore(t)

	parent := &types.Issue{
		ID:        "ga-transitive-parent",
		Title:     "Existing Parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, actor); err != nil {
		t.Fatalf("CreateIssue(parent): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "mid", Title: "Middle", Type: "task"},
			{Key: "child", Title: "Child", Type: "task", ParentID: parent.ID},
		},
		Edges: []GraphApplyEdge{
			{FromID: parent.ID, ToKey: "mid", Type: "blocks"},
			{FromKey: "mid", ToKey: "child", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected external parent transitive blocking path to be rejected")
	}
	// Confirmed against a real Dolt test server (2026-10 coordinator
	// follow-up): see the sibling comment in
	// TestExecuteGraphApplyRejectsStoredPrefixParentBlockingPath above for
	// why this checks for "cycle" rather than a parent-path phrase.
	if got, want := err.Error(), "cycle"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE title IN ('Middle', 'Child')").Scan(&count); err != nil {
		t.Fatalf("query plan issue rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("plan issue rows after rejected transitive path = %d, want 0", count)
	}
}

// TestExecuteGraphApplyRejectsCombinedSchedulingCycle restores the deleted
// fake-store TestExecuteGraphApplyUnitRejectsCombinedSchedulingCycle: a cycle
// that mixes a blocking edge, an EXPLICIT (not implicit-via-parent_key)
// parent-child edge, and a conditional-blocks edge. The explicit
// parent-child leg is deliberately not a "cycle relevant" type for
// validateGraphApplyLocalCycles's plan-local preflight
// (graphApplyCycleRelevantDependencyType only treats blocks/
// conditional-blocks as such), so this cycle is invisible to that pure
// check and is caught only by the real apply path's whole-graph gate.
func TestExecuteGraphApplyRejectsCombinedSchedulingCycle(t *testing.T) {
	ctx, db := withGraphApplyTestStore(t)

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
			{Key: "c", Title: "C", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "a", ToKey: "b", Type: string(types.DepBlocks)},
			{FromKey: "b", ToKey: "c", Type: string(types.DepParentChild)},
			{FromKey: "c", ToKey: "a", Type: string(types.DepConditionalBlocks)},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("combined scheduling cycle error = %v, want rejection", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE title IN ('A', 'B', 'C')").Scan(&count); err != nil {
		t.Fatalf("query issue rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("issue rows after rejected combined scheduling cycle = %d, want 0", count)
	}
}

// TestExecuteGraphApplyRejectsCycleHiddenInInlineDep restores the deleted
// fake-store TestExecuteGraphApplyUnitFinalGateCatchesCycleHiddenFromPerEdgeChecks's
// "inline parent-child dependency" subcase: the cycle-closing parent-child
// hop is a per-node inline Dep (node.Deps), never a top-level plan Edge, so
// it is invisible to validateGraphApplyLocalCycles (which only walks
// plan.Edges) and to any check that only looks at explicit edges. Only the
// real apply path's final whole-graph gate, which sees every dependency the
// batch will write (inline or not), can catch it.
func TestExecuteGraphApplyRejectsCycleHiddenInInlineDep(t *testing.T) {
	ctx, db := withGraphApplyTestStore(t)

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task", Deps: []GraphApplyNodeDep{{Type: string(types.DepParentChild), Target: "c"}}},
			{Key: "c", Title: "C", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "a", ToKey: "b", Type: string(types.DepBlocks)},
			{FromKey: "c", ToKey: "a", Type: string(types.DepConditionalBlocks)},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %v, want cycle rejection via the inline parent-child dep", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE title IN ('A', 'B', 'C')").Scan(&count); err != nil {
		t.Fatalf("query issue rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("issue rows after rejected plan = %d, want 0", count)
	}
}

// TestExecuteGraphApplyFinalGateIncludesImplicitNodeParent restores the
// deleted fake-store TestExecuteGraphApplyUnitFinalGateIncludesImplicitNodeParent,
// recast as a rejection: the old test introspected the fake store's captured
// cycle-check edge set directly, which has no real-store equivalent, so this
// pins the same claim (the final gate folds in a node's IMPLICIT
// parent-child edge, not only explicit plan edges) by constructing a cycle
// that closes ONLY through that implicit edge — b's parent is an existing
// external issue addressed by ParentID, which validateGraphApplyLocalCycles
// cannot see at all (it only models an implicit parent for a LOCAL,
// plan-key parent) — so a false negative here would mean the final gate
// silently dropped the implicit edge.
func TestExecuteGraphApplyFinalGateIncludesImplicitNodeParent(t *testing.T) {
	ctx, db := withGraphApplyTestStore(t)

	existingParent := &types.Issue{
		ID:        "ga-final-gate-parent",
		Title:     "Existing Parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, existingParent, actor); err != nil {
		t.Fatalf("CreateIssue(parent): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task", ParentID: existingParent.ID},
		},
		Edges: []GraphApplyEdge{
			{FromID: existingParent.ID, ToKey: "a", Type: string(types.DepBlocks)},
			{FromKey: "a", ToKey: "b", Type: string(types.DepBlocks)},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected cycle closed through b's implicit parent-child edge to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %q, want cycle rejection", err.Error())
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE title IN ('A', 'B')").Scan(&count); err != nil {
		t.Fatalf("query issue rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("issue rows after rejected plan = %d, want 0", count)
	}
}

// TestExecuteGraphApplyRejectsBlockingThroughPlannedHierarchyRegardlessOfOrder
// restores the deleted fake-store
// TestExecuteGraphApplyUnitRejectsBlockingThroughPlannedHierarchyRegardlessOfOrder:
// a blocking edge onto a planned ancestor must be rejected whether the
// hierarchy is stated as explicit parent-child plan edges or as per-node
// inline Deps, and regardless of which edge the plan lists first — the
// blocking edge deliberately comes before the parent-child edges/deps in
// both subcases, so a check that stops at the first-seen edge rather than
// resolving the whole planned hierarchy would miss it.
func TestExecuteGraphApplyRejectsBlockingThroughPlannedHierarchyRegardlessOfOrder(t *testing.T) {
	tests := []struct {
		name string
		plan *GraphApplyPlan
	}{
		{
			name: "explicit edges",
			plan: &GraphApplyPlan{
				Nodes: []GraphApplyNode{
					{Key: "grand", Title: "Grand", Type: "task"},
					{Key: "parent", Title: "Parent", Type: "task"},
					{Key: "child", Title: "Child", Type: "task"},
				},
				Edges: []GraphApplyEdge{
					{FromKey: "child", ToKey: "grand", Type: "conditional-blocks"}, // Deliberately first.
					{FromKey: "child", ToKey: "parent", Type: "parent-child"},
					{FromKey: "parent", ToKey: "grand", Type: "parent-child"},
				},
			},
		},
		{
			name: "inline deps",
			plan: &GraphApplyPlan{
				Nodes: []GraphApplyNode{
					{Key: "grand", Title: "Grand", Type: "task"},
					{Key: "parent", Title: "Parent", Type: "task", Deps: []GraphApplyNodeDep{{Type: "parent-child", Target: "grand"}}},
					{Key: "child", Title: "Child", Type: "task", Deps: []GraphApplyNodeDep{
						{Type: "blocks", Target: "grand"}, // Deliberately first.
						{Type: "parent-child", Target: "parent"},
					}},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, db := withGraphApplyTestStore(t)
			_, err := executeGraphApply(ctx, tt.plan, GraphApplyOptions{})
			if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
				t.Fatalf("error = %v, want planned-ancestor rejection", err)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
				t.Fatalf("query issue rows: %v", err)
			}
			if count != 0 {
				t.Fatalf("issue rows after rejected plan = %d, want 0", count)
			}
		})
	}
}
