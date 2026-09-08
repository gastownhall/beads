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

	if err := validateGraphApplyPlan(plan, nil); err != nil {
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
	if got, want := err.Error(), "planned blocking dependencies create a path from parent"; !strings.Contains(got, want) {
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
	if !strings.Contains(err.Error(), "parent-child") {
		t.Fatalf("error = %q, want parent-child duplicate rejection", err.Error())
	}
}
