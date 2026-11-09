//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEmbeddedCrossPrefixHierarchyConflictRefused pins the write-gate half of
// GH#8299: a target this database holds under a different prefix is a LOCAL
// edge, so the hierarchy rules bind it like any same-prefix edge. A blocking
// dependency on the source's own ancestor or descendant — through parent-child
// edges that themselves span prefixes — must be refused wherever it enters:
// per-edge add, create-with-deps, and batch apply, including the batch end
// gate that re-validates edges a LATER item made invalid and rolls the whole
// request back. On base every cross-prefix edge was filed external, no gate
// ever saw the hierarchy, and the invalid blocking edges persisted.
func TestEmbeddedCrossPrefixHierarchyConflictRefused(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// depEdgeCount counts every stored edge between the pair, in either
	// target column, on the issue plane.
	depEdgeCount := func(t *testing.T, te *testEnv, source, target string) int {
		t.Helper()
		var n int
		te.queryScalar(t, t.Context(),
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND "+
				"(depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external = ?)",
			[]any{source, target, target, target}, &n)
		return n
	}

	t.Run("perEdgeAdd", func(t *testing.T) {
		te := newTestEnv(t, "a")
		ctx := t.Context()
		const child, parent = "a-hc", "b-hp"
		createCrossPrefixPair(t, te, child, parent)
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("parent-child edge %s -> %s: %v", child, parent, err)
		}

		err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: parent, Type: types.DepBlocks,
		}, "tester")
		var conflict *domain.DependencyHierarchyConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("blocks edge from %s on its own ancestor %s: error = %v, "+
				"want *DependencyHierarchyConflictError", child, parent, err)
		}
		if !conflict.BlockerIsAncestor {
			t.Errorf("conflict = %#v, want BlockerIsAncestor true: %s is %s's parent across prefixes",
				conflict, parent, child)
		}
		if got := depEdgeCount(t, te, child, parent); got != 1 {
			t.Errorf("stored edges %s -> %s = %d after the refusal, want 1 (the parent-child edge only)",
				child, parent, got)
		}
	})

	t.Run("createWithDeps", func(t *testing.T) {
		te := newTestEnv(t, "a")
		ctx := t.Context()
		const parent = "b-cp"
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: parent, Title: "cross-prefix ancestor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", parent, err)
		}
		const source = "a-csrc"
		batch := []*types.Issue{{
			ID: source, Title: "created with a conflicting dep", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: parent, Type: types.DepParentChild},
				{DependsOnID: parent, Type: types.DepBlocks},
			},
		}}
		err := te.store.CreateIssuesWithFullOptions(ctx, batch, "tester", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		})
		var conflict *domain.DependencyHierarchyConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("create with a blocks dep on its own ancestor %s: error = %v, "+
				"want *DependencyHierarchyConflictError", parent, err)
		}
		var created int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{source}, &created)
		if created != 0 {
			t.Errorf("issues row for %s persisted after the create refused: %d rows", source, created)
		}
		if got := depEdgeCount(t, te, source, parent); got != 0 {
			t.Errorf("stored edges %s -> %s = %d after the create refused, want 0 (the create rolled back)",
				source, parent, got)
		}
	})

	t.Run("batchApplyEndGate", func(t *testing.T) {
		te := newTestEnv(t, "a")
		ctx := t.Context()
		applier, err := te.store.BatchApplier()
		if err != nil {
			t.Fatalf("BatchApplier(): %v", err)
		}
		// The hierarchy spans prefixes: child (a-) under parent (b-) under
		// grand (b-), so the blocking edge child -> grand names its own
		// ancestor and the parent-child edge that proves it crosses prefixes.
		const child, parent, grand = "a-eg-child", "b-eg-parent", "b-eg-grand"
		for _, id := range []string{child, parent, grand} {
			if err := te.store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			}, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", id, err)
			}
		}
		gate := func(first publicops.DependencyType) publicops.ApplyBatchRequest {
			return publicops.ApplyBatchRequest{
				Actor: "apply-writer",
				Items: []publicops.ApplyItem{
					{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
						Source: publicops.Ref{ID: child}, Target: publicops.Ref{ID: grand}, Type: first}},
					{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
						Source: publicops.Ref{ID: child}, Target: publicops.Ref{ID: parent}, Type: publicops.DepParentChild}},
					{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
						Source: publicops.Ref{ID: parent}, Target: publicops.Ref{ID: grand}, Type: publicops.DepParentChild}},
				},
			}
		}

		// The blocking edge is legal when it is written — no parent-child edge
		// exists yet — and only the END GATE, re-validating the graph the whole
		// request produced, refuses it.
		_, err = applier.ApplyBatch(ctx, gate(publicops.DepBlocks))
		var conflict *publicops.DependencyHierarchyConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("blocking edge made invalid by later parent-child edges in the same batch: "+
				"error = %v, want *DependencyHierarchyConflictError from the END GATE", err)
		}
		if conflict.IssueID != child || conflict.BlockerID != grand {
			t.Errorf("conflict = %#v, want IssueID %s gated on BlockerID %s", conflict, child, grand)
		}
		var itemErr *publicops.ItemError
		if !errors.As(err, &itemErr) || itemErr.Index != 0 || itemErr.Kind != publicops.ItemDepAdd {
			t.Errorf("ItemError = %#v (err = %v), want Index 0 and Kind %q — the blocking edge, "+
				"at the position the caller declared it", itemErr, err, publicops.ItemDepAdd)
		}
		for _, edge := range [][2]string{{child, grand}, {child, parent}, {parent, grand}} {
			if got := depEdgeCount(t, te, edge[0], edge[1]); got != 0 {
				t.Errorf("stored edges %s -> %s = %d after the end gate refused, want 0 (the batch rolled back)",
					edge[0], edge[1], got)
			}
		}

		// The same request with the first edge non-blocking lands in full: a
		// hierarchy is only a conflict for an edge that gates.
		if _, err := applier.ApplyBatch(ctx, gate(publicops.DepRelated)); err != nil {
			t.Fatalf("the same three edges with a related edge first: %v", err)
		}
		for _, edge := range [][2]string{{child, grand}, {child, parent}, {parent, grand}} {
			if got := depEdgeCount(t, te, edge[0], edge[1]); got != 1 {
				t.Errorf("stored edges %s -> %s = %d after the control batch landed, want 1",
					edge[0], edge[1], got)
			}
		}
	})
}
