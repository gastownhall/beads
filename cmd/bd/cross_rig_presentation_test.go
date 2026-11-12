package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// crossRigEdgeReader answers ReadEdges with the stored edges of one anchor
// and counts the calls.
type crossRigEdgeReader struct {
	edges []*types.Dependency
	err   error
	calls int
}

func (r *crossRigEdgeReader) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	r.calls++
	if r.err != nil {
		return issueops.EdgeReadResult{}, r.err
	}
	var edges []*types.Dependency
	for _, dep := range r.edges {
		if len(req.Types) == 0 || slices.Contains(req.Types, dep.Type) {
			edges = append(edges, dep)
		}
	}
	return issueops.EdgeReadResult{Anchors: []issueops.AnchorEdges{{ID: req.IDs[0], Edges: edges}}}, nil
}

// withPresentationResolver points the show and dep list views at a resolver
// that answers from issues and records the ids each call asked for.
func withPresentationResolver(t *testing.T, issues map[string]*types.Issue) *[][]string {
	t.Helper()
	var asked [][]string
	old := crossRigPresentationResolver
	crossRigPresentationResolver = func() externaldeps.IssueResolver {
		return func(_ context.Context, ids []string) (map[string]*types.Issue, map[string]string, error) {
			asked = append(asked, slices.Clone(ids))
			found := map[string]*types.Issue{}
			unavailable := map[string]string{}
			for _, id := range ids {
				if issue := issues[id]; issue != nil {
					copied := *issue
					found[id] = &copied
				} else {
					unavailable[id] = "not found"
				}
			}
			return found, unavailable, nil
		}
	}
	t.Cleanup(func() { crossRigPresentationResolver = old })
	return &asked
}

func crossRigShownEdges() []*types.Dependency {
	return []*types.Dependency{
		{IssueID: "aa-1", DependsOnID: "aa-2", Type: types.DepBlocks},
		{IssueID: "aa-1", DependsOnID: "bb-1", Type: types.DepBlocks},
		{IssueID: "aa-1", DependsOnID: "bb-2", Type: types.DepRelated},
		{IssueID: "aa-1", DependsOnID: "cc-9", Type: types.DepBlocks},
		{IssueID: "aa-1", DependsOnID: "external:proj:cap", Type: types.DepBlocks},
	}
}

func crossRigForeignIssues() map[string]*types.Issue {
	return map[string]*types.Issue{
		"bb-1": {ID: "bb-1", Title: "Foreign blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Description: "long text"},
		"bb-2": {ID: "bb-2", Title: "Foreign peer", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeBug, Description: "closed text"},
	}
}

// TestForeignDependencyRowsListResolvedCrossRigTargets pins the dep list and
// show rows for GH#5784: a resolved bare cross-prefix target gets one row per
// edge with its own edge type and external set; a local neighbor already
// shown, an external: capability ref, and an unresolved target get none.
func TestForeignDependencyRowsListResolvedCrossRigTargets(t *testing.T) {
	asked := withPresentationResolver(t, crossRigForeignIssues())
	reader := &crossRigEdgeReader{edges: crossRigShownEdges()}
	shown := []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "aa-2"}, DependencyType: types.DepBlocks}}

	rows := foreignDependencyRows(context.Background(), reader, "aa-1", nil, shown, crossRigPresentationResolver())
	var got []string
	for _, row := range rows {
		if !row.External {
			t.Errorf("row %s: External = false, want true", row.ID)
		}
		got = append(got, row.ID+" via "+string(row.DependencyType))
	}
	if want := []string{"bb-1 via blocks", "bb-2 via related"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if want := [][]string{{"bb-1", "bb-2", "cc-9"}}; !slices.EqualFunc(*asked, want, slices.Equal[[]string]) {
		t.Fatalf("resolver asked %v, want %v", *asked, want)
	}

	rows = foreignDependencyRows(context.Background(), reader, "aa-1", []types.DependencyType{types.DepRelated}, shown, crossRigPresentationResolver())
	if len(rows) != 1 || rows[0].ID != "bb-2" {
		t.Fatalf("--type related rows = %+v, want only bb-2", rows)
	}

	if rows := foreignDependencyRows(context.Background(), &crossRigEdgeReader{err: errors.New("boom")}, "aa-1", nil, nil, crossRigPresentationResolver()); rows != nil {
		t.Fatalf("edge read error rows = %+v, want none", rows)
	}

	line := formatDependencyLine("→", &types.IssueWithDependencyMetadata{Issue: *crossRigForeignIssues()["bb-1"], DependencyType: types.DepBlocks, External: true})
	if !strings.Contains(line, "bb-1") || !strings.Contains(line, "(external)") {
		t.Fatalf("show line %q lacks the external marker", line)
	}
	if line := formatDependencyLine("→", shown[0]); strings.Contains(line, "(external)") {
		t.Fatalf("local show line %q carries the external marker", line)
	}
}

// TestShowForeignDependencyDetailsTakeRowsOffTheUnresolvedCount pins the
// --json show view: resolved foreign rows join dependencies (brief form under
// --brief-deps), leave unresolvable_dependencies counting only the edges still
// unresolved, and nothing is read when no edge is unresolved.
func TestShowForeignDependencyDetailsTakeRowsOffTheUnresolvedCount(t *testing.T) {
	withPresentationResolver(t, crossRigForeignIssues())
	for _, brief := range []bool{false, true} {
		reader := &crossRigEdgeReader{edges: crossRigShownEdges()}
		three := int64(3)
		details := &types.IssueDetails{
			Issue:                    types.Issue{ID: "aa-1"},
			Dependencies:             []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "aa-2"}, DependencyType: types.DepBlocks}},
			UnresolvableDependencies: &three,
		}
		appendForeignDependencyDetails(context.Background(), func() (issueops.EdgeReader, error) { return reader, nil }, details, brief)
		blob, err := json.Marshal(details)
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			Dependencies []map[string]any `json:"dependencies"`
			Unresolvable *int64           `json:"unresolvable_dependencies"`
		}
		if err := json.Unmarshal(blob, &view); err != nil {
			t.Fatal(err)
		}
		if view.Unresolvable == nil || *view.Unresolvable != 1 {
			t.Fatalf("brief=%v unresolvable_dependencies = %v, want 1 (cc-9 and the external ref)", brief, view.Unresolvable)
		}
		if len(view.Dependencies) != 3 {
			t.Fatalf("brief=%v dependencies = %v, want aa-2, bb-1, bb-2", brief, view.Dependencies)
		}
		if _, ok := view.Dependencies[0]["external"]; ok {
			t.Errorf("brief=%v local row carries external", brief)
		}
		for _, row := range view.Dependencies[1:] {
			if row["external"] != true {
				t.Errorf("brief=%v row %v: external != true", brief, row["id"])
			}
			if _, hasDesc := row["description"]; hasDesc == brief {
				t.Errorf("brief=%v row %v: description present = %v", brief, row["id"], hasDesc)
			}
		}
	}

	one := int64(1)
	details := &types.IssueDetails{Issue: types.Issue{ID: "aa-1"}, UnresolvableDependencies: &one}
	reader := &crossRigEdgeReader{edges: []*types.Dependency{{IssueID: "aa-1", DependsOnID: "bb-1", Type: types.DepBlocks}}}
	appendForeignDependencyDetails(context.Background(), func() (issueops.EdgeReader, error) { return reader, nil }, details, false)
	if details.UnresolvableDependencies != nil || len(details.Dependencies) != 1 {
		t.Fatalf("all resolved: unresolvable = %v, deps = %d, want nil and 1", details.UnresolvableDependencies, len(details.Dependencies))
	}

	idle := &crossRigEdgeReader{edges: crossRigShownEdges()}
	appendForeignDependencyDetails(context.Background(), func() (issueops.EdgeReader, error) { return idle, nil }, &types.IssueDetails{Issue: types.Issue{ID: "aa-1"}}, false)
	short := depCounts{deps: depCount{n: 5}}
	full := []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "aa-2"}}}
	if got := withForeignDependencyRows(context.Background(), func() (issueops.EdgeReader, error) { return idle, nil }, "aa-1", short, full, errors.New("listing failed")); len(got) != 1 {
		t.Fatalf("failed listing grew to %d rows", len(got))
	}
	if got := withForeignDependencyRows(context.Background(), func() (issueops.EdgeReader, error) { return idle, nil }, "aa-1", depCounts{deps: depCount{n: 1}}, full, nil); len(got) != 1 {
		t.Fatalf("complete listing grew to %d rows", len(got))
	}
	if idle.calls != 0 {
		t.Fatalf("edge reads with nothing unresolved = %d, want 0", idle.calls)
	}
	if got := withForeignDependencyRows(context.Background(), func() (issueops.EdgeReader, error) { return idle, nil }, "aa-1", short, full, nil); len(got) != 3 {
		t.Fatalf("short text listing = %d rows, want aa-2, bb-1, bb-2", len(got))
	}
}

// TestDepTreeMarksForeignLeavesAndBadgesByThePolicyRule pins the dep tree
// text for GH#5784 AC-7: a foreign leaf reads "(external)", an unresolved one
// names its id and reason, and only a foreign blocks or conditional-blocks
// edge (open or unreadable) badges the root [BLOCKED]; waits-for, tracks,
// related and parent-child foreign leaves leave it [READY].
func TestDepTreeMarksForeignLeavesAndBadgesByThePolicyRule(t *testing.T) {
	root := func() *types.TreeNode {
		return &types.TreeNode{Issue: types.Issue{ID: "aa-1", Title: "Source", Status: types.StatusOpen}}
	}
	leaf := func(id string, edge types.DependencyType, status types.Status, unresolved string) *types.TreeNode {
		node := &types.TreeNode{Issue: types.Issue{ID: id, Title: "Foreign " + id, Status: status}, Depth: 1, ParentID: "aa-1", EdgeFromParent: edge, External: true, Unresolved: unresolved}
		if unresolved != "" {
			node.Title = id
		}
		return node
	}
	render := func(nodes ...*types.TreeNode) string {
		return captureStdout(t, func() error { renderTree(nodes, 10, "down"); return nil })
	}

	out := render(root(),
		leaf("bb-1", types.DepWaitsFor, types.StatusOpen, ""),
		leaf("bb-2", types.DepTracks, types.StatusOpen, ""),
		leaf("bb-3", types.DepRelated, "", "database is locked"),
		leaf("bb-4", types.DepParentChild, types.StatusOpen, ""),
		leaf("bb-5", types.DepBlocks, types.StatusClosed, ""))
	if !strings.Contains(out, "[READY]") || strings.Contains(out, "[BLOCKED]") {
		t.Fatalf("non-blocking foreign leaves badged the root:\n%s", out)
	}
	for _, want := range []string{"bb-1: Foreign bb-1", "bb-5: Foreign bb-5", "bb-3: unresolved: database is locked"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree lacks %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "(external)"); got != 5 {
		t.Fatalf("(external) markers = %d, want 5:\n%s", got, out)
	}

	for _, blocker := range []*types.TreeNode{
		leaf("bb-6", types.DepBlocks, types.StatusOpen, ""),
		leaf("bb-6", types.DepConditionalBlocks, types.StatusBlocked, ""),
		leaf("bb-6", types.DepBlocks, "", `no route for prefix "bb"`),
	} {
		if out := render(root(), blocker); !strings.Contains(out, "[BLOCKED]") {
			t.Fatalf("foreign %s leaf (%q, unresolved %q) left the root unbadged:\n%s", blocker.EdgeFromParent, blocker.Status, blocker.Unresolved, out)
		}
	}
}
