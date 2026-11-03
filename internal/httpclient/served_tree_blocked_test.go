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
	for _, side := range []struct {
		name  string
		nodes []*types.TreeNode
	}{{"direct", direct}, {"served", served}} {
		rows := treeIsBlockedJSON(t, side.nodes)
		if len(rows) != len(want) {
			t.Fatalf("%s tree = %v, want one row each for %s, %s, %s", side.name, rows, child, epic, blocker)
		}
		for _, row := range rows {
			id, _ := row[0].(string)
			if w, ok := want[id]; !ok || row[1] != w {
				t.Errorf("%s tree: %s is_blocked = %#v, want %#v", side.name, id, row[1], want[id])
			}
		}
		for _, node := range side.nodes {
			if node.IsBlocked != (want[node.ID] == true) {
				t.Errorf("%s tree: %s IsBlocked = %v", side.name, node.ID, node.IsBlocked)
			}
		}
	}
}
