package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestCrossPrefixLocalHierarchyRefusal is the hierarchy half of GH#8299: a
// parent and child of different prefixes held in one database have a local
// hierarchy, so a blocking edge between them is refused on every write path
// exactly as it is for same-prefix issues. Each path used to skip the check
// for a foreign prefix.
func TestCrossPrefixLocalHierarchyRefusal(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seed := func(t *testing.T, ids ...string) {
		t.Helper()
		var issues []*types.Issue
		for _, id := range ids {
			issues = append(issues, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
		}
		if err := store.CreateIssuesWithFullOptions(ctx, issues, "tester",
			storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("seed %v: %v", ids, err)
		}
	}
	requireHierarchyRefusal := func(t *testing.T, err error, ancestor bool) {
		t.Helper()
		var conflict *publicops.DependencyHierarchyConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want a DependencyHierarchyConflictError", err)
		}
		if conflict.BlockerIsAncestor != ancestor {
			t.Errorf("BlockerIsAncestor = %v, want %v (%v)", conflict.BlockerIsAncestor, ancestor, err)
		}
	}
	edgeCount := func(t *testing.T, source, target string) int {
		t.Helper()
		var n int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
			source, target).Scan(&n); err != nil {
			t.Fatalf("count %s -> %s: %v", source, target, err)
		}
		return n
	}

	t.Run("explicit add", func(t *testing.T) {
		grand, parent, child := "xbadd-grand", "xdadd-parent", "xaadd-child"
		seed(t, grand, parent, child)
		for _, edge := range [][2]string{{parent, grand}, {child, parent}} {
			if err := store.AddDependency(ctx, &types.Dependency{IssueID: edge[0], DependsOnID: edge[1], Type: types.DepParentChild}, "tester"); err != nil {
				t.Fatalf("add parent-child %s -> %s: %v", edge[0], edge[1], err)
			}
		}
		err := store.AddDependency(ctx, &types.Dependency{IssueID: child, DependsOnID: grand, Type: types.DepBlocks}, "tester")
		requireHierarchyRefusal(t, err, true)
		err = store.AddDependency(ctx, &types.Dependency{IssueID: grand, DependsOnID: child, Type: types.DepBlocks}, "tester")
		requireHierarchyRefusal(t, err, false)
		for _, edge := range [][2]string{{child, grand}, {grand, child}} {
			if n := edgeCount(t, edge[0], edge[1]); n != 0 {
				t.Errorf("refused edge %s -> %s was written (%d rows)", edge[0], edge[1], n)
			}
		}
	})

	t.Run("batch", func(t *testing.T) {
		grand, parent, child, other := "xbbatch-grand", "xdbatch-parent", "xabatch-child", "xcbatch-other"
		seed(t, grand, parent, child, other)
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: parent, DependsOnID: grand, Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("add parent-child: %v", err)
		}
		applier, err := store.BatchApplier()
		if err != nil {
			t.Fatalf("BatchApplier(): %v", err)
		}
		depAdd := func(source, target string, typ publicops.DependencyType) publicops.ApplyItem {
			return publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
				Source: publicops.Ref{ID: source}, Target: publicops.Ref{ID: target}, Type: typ,
			}}
		}
		// The blocking edge onto the grandparent precedes the parent-child
		// edge that makes it a hierarchy conflict, so only the end gate can
		// see it.
		_, err = applier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
			Actor: "tester",
			Items: []publicops.ApplyItem{
				depAdd(other, grand, publicops.DepBlocks),
				depAdd(child, grand, publicops.DepBlocks),
				depAdd(child, parent, publicops.DepParentChild),
			},
		})
		var itemErr *publicops.ItemError
		if !errors.As(err, &itemErr) || itemErr.Index != 1 {
			t.Fatalf("ApplyBatch err = %v, want an ItemError for item 1", err)
		}
		requireHierarchyRefusal(t, err, true)
		for _, edge := range [][2]string{{other, grand}, {child, grand}, {child, parent}} {
			if n := edgeCount(t, edge[0], edge[1]); n != 0 {
				t.Errorf("refused batch committed %s -> %s (%d rows)", edge[0], edge[1], n)
			}
		}
	})

	t.Run("create with deps", func(t *testing.T) {
		grand, parent, child := "xbcreate-grand", "xdcreate-parent", "xacreate-child"
		seed(t, grand, parent)
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: parent, DependsOnID: grand, Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("add parent-child: %v", err)
		}
		err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: child, Title: child, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: parent, Type: types.DepParentChild},
				{DependsOnID: grand, Type: types.DepBlocks},
			},
		}}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true})
		requireHierarchyRefusal(t, err, true)
		var n int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", child).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", child, err)
		}
		if n != 0 {
			t.Errorf("refused create committed %s", child)
		}
	})
}
