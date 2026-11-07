//go:build cgo

package httpclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// treeOverBoth walks req against the reference store directly and through the
// http client, and returns both answers.
func treeOverBoth(t *testing.T, ctx context.Context, e *servedEnv, req issueops.WalkTreeRequest) (direct, served []*types.TreeNode) {
	t.Helper()
	for _, side := range []struct {
		name   string
		source interface {
			TreeWalker() (issueops.TreeWalker, error)
		}
		out *[]*types.TreeNode
	}{{"direct", e.reference, &direct}, {"served", e.subject, &served}} {
		walker, err := side.source.TreeWalker()
		if err != nil {
			t.Fatalf("%s TreeWalker: %v", side.name, err)
		}
		result, err := walker.WalkTree(ctx, req)
		if err != nil {
			t.Fatalf("%s WalkTree(%+v): %v", side.name, req, err)
		}
		*side.out = result.Nodes
	}
	return direct, served
}

// treeIsBlockedJSON serializes nodes and returns, per row, the row's id and its
// is_blocked member (nil when the member is absent).
func treeIsBlockedJSON(t *testing.T, nodes []*types.TreeNode) [][2]any {
	t.Helper()
	raw, err := json.Marshal(nodes)
	if err != nil {
		t.Fatalf("marshal tree: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal tree: %v", err)
	}
	out := make([][2]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, [2]any{row["id"], row["is_blocked"]})
	}
	return out
}

// TestServedTreeCarriesTheInheritedBlockedFlag is #6630's graph over both
// read paths: open blocker B, epic E depending on B, child C of E. The direct
// and served walks of C both answer is_blocked: true on C and E and omit the
// member on B.
func TestServedTreeCarriesTheInheritedBlockedFlag(t *testing.T) {
	e := newServedEnv(t, "tib")
	ctx := t.Context()

	blocker, epic, child := e.prefix+"-b", e.prefix+"-e", e.prefix+"-c"
	for _, seed := range []*types.Issue{
		{ID: blocker, Title: "blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: epic, Title: "epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
		{ID: child, Title: "child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := e.createIssue(ctx, seed, "seed"); err != nil {
			t.Fatalf("seed %s: %v", seed.ID, err)
		}
	}
	for _, edge := range []*types.Dependency{
		{IssueID: epic, DependsOnID: blocker, Type: types.DepBlocks},
		{IssueID: child, DependsOnID: epic, Type: types.DepParentChild},
	} {
		if err := e.addDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}

	want := map[string]any{child: true, epic: true, blocker: nil}
	direct, served := treeOverBoth(t, ctx, e, issueops.WalkTreeRequest{RootID: child, MaxDepth: 50})
	wantTreeBlocked(t, "direct", direct, want, 3)
	wantTreeBlocked(t, "served", served, want, 3)
}

// wantTreeBlocked checks every row of nodes, repeats included, against want
// (per id: true, or nil for an omitted member) both as decoded and as
// serialized, and that the walk has rows rows.
func wantTreeBlocked(t *testing.T, side string, nodes []*types.TreeNode, want map[string]any, rows int) {
	t.Helper()
	got := treeIsBlockedJSON(t, nodes)
	if len(got) != rows {
		t.Fatalf("%s tree = %v, want %d rows", side, got, rows)
	}
	for _, row := range got {
		id, _ := row[0].(string)
		if w, ok := want[id]; !ok || row[1] != w {
			t.Errorf("%s tree: %s is_blocked = %#v, want %#v", side, id, row[1], want[id])
		}
	}
	for _, node := range nodes {
		if node.IsBlocked != (want[node.ID] == true) {
			t.Errorf("%s tree: %s IsBlocked = %v (deduped %v)", side, node.ID, node.IsBlocked, node.Deduped)
		}
	}
}

// TestServedTreeStampsEveryOccurrenceOfABlockedNode (bd-42wxtt): root R
// depends on M and S, M also depends on S, so S is expanded once and repeated
// as a Deduped stub; S waits on open X. R also depends on wisp W, which waits
// on open Y through wisp_dependencies. S and W each carry a true stored flag
// backed by an edge of their own family, so every row of each, the stub's
// independent copy included, answers is_blocked: true on both read paths.
func TestServedTreeStampsEveryOccurrenceOfABlockedNode(t *testing.T) {
	e := newServedEnv(t, "tso")
	ctx := t.Context()

	id := func(s string) string { return e.prefix + "-" + s }
	open := func(s string) *types.Issue {
		return &types.Issue{ID: id(s), Title: s, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	for _, s := range []string{"r", "m", "s", "x", "y"} {
		if err := e.createIssue(ctx, open(s), "seed"); err != nil {
			t.Fatalf("seed %s: %v", s, err)
		}
	}
	if err := e.createWisp(ctx, open("w"), "seed"); err != nil {
		t.Fatalf("seed wisp: %v", err)
	}
	for _, edge := range [][2]string{{"r", "m"}, {"r", "s"}, {"m", "s"}, {"s", "x"}, {"r", "w"}, {"w", "y"}} {
		dep := &types.Dependency{IssueID: id(edge[0]), DependsOnID: id(edge[1]), Type: types.DepBlocks}
		if err := e.addDependency(ctx, dep, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge[0], edge[1], err)
		}
	}

	want := map[string]any{id("r"): true, id("m"): true, id("s"): true, id("w"): true, id("x"): nil, id("y"): nil}
	direct, served := treeOverBoth(t, ctx, e, issueops.WalkTreeRequest{RootID: id("r"), MaxDepth: 50})
	for _, side := range []struct {
		name  string
		nodes []*types.TreeNode
	}{{"direct", direct}, {"served", served}} {
		stubs, wisps := 0, 0
		for _, node := range side.nodes {
			if node.ID == id("s") && node.Deduped {
				stubs++
			}
			if node.ID == id("w") {
				wisps++
			}
		}
		if stubs != 1 || wisps != 1 {
			t.Fatalf("%s tree has %d Deduped %s stubs and %d %s rows, want one each: %v", side.name, stubs, id("s"), wisps, id("w"), treeIsBlockedJSON(t, side.nodes))
		}
		wantTreeBlocked(t, side.name, side.nodes, want, 7)
	}
}
