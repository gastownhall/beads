package conformance

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// testSearchDescendants pins storage.SearchDescendants, the `bd list --parent`
// tree walk (GH#3415): it must answer exactly what the node-by-node
// SearchIssues(ParentID=…) walk answers over the same store. A store that
// offers the optional storage.DescendantSearcher capability (the in-tree
// embedded and Dolt server stores) is held to that through the capability,
// which reads the whole tree in one read transaction; a store that does not,
// as an out-of-tree backend need not, is held to it through the node-by-node
// fallback bd walks it with. The capability's absence is therefore no failure
// here: the in-tree stores' compile-time storage.DescendantSearcher assertions
// pin that they offer it.
//
// The tree covers every way a ParentID search reaches a node:
//
//	dw-r.1 (root, epic)
//	├── dw-r.1.1   edge-less dotted-ID child (the ParentID filter's second arm)
//	├── dw-a
//	│   ├── dw-a1
//	│   │   └── dw-r   whose dotted-ID arm finds the edge-less root dw-r.1
//	│   │              again: a cycle back to the root
//	│   └── dw-w       wisp
//	│       └── dw-w1  wisp
//	└── dw-c       closed, mid-level
//	    └── dw-c1
//
// The cycle goes through the dotted-ID arm because AddDependency refuses a
// parent-child edge cycle. With Status: open, the closed dw-c is not a child
// at its level, so neither it nor its subtree is walked.
func testSearchDescendants(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.SetConfig(c, "issue_prefix", "dw"))
	for _, issue := range []*types.Issue{
		{ID: "dw-r.1", Title: "root", IssueType: types.TypeEpic},
		{ID: "dw-r.1.1", Title: "dotted child"},
		{ID: "dw-a", Title: "a"},
		{ID: "dw-a1", Title: "a1"},
		{ID: "dw-r", Title: "back to the root"},
		{ID: "dw-w", Title: "wisp", Ephemeral: true},
		{ID: "dw-w1", Title: "wisp child", Ephemeral: true},
		{ID: "dw-c", Title: "closed mid-level", Status: types.StatusClosed},
		{ID: "dw-c1", Title: "under the closed one"},
	} {
		must(t, s.CreateIssue(c, withDefaults(issue), "a"))
	}
	for _, edge := range [][2]string{
		{"dw-a", "dw-r.1"},
		{"dw-c", "dw-r.1"},
		{"dw-a1", "dw-a"},
		{"dw-r", "dw-a1"},
		{"dw-w", "dw-a"},
		{"dw-w1", "dw-w"},
		{"dw-c1", "dw-c"},
	} {
		must(t, s.AddDependency(c, &types.Dependency{IssueID: edge[0], DependsOnID: edge[1], Type: types.DepParentChild}, "a"))
	}

	// perNode is the walk SearchDescendants replaces, written out here rather
	// than through storage.WalkDescendants so the two do not share a body.
	perNode := func(t *testing.T, filter types.IssueFilter) []string {
		t.Helper()
		seen := map[string]bool{}
		var walk func(parentID string)
		walk = func(parentID string) {
			level := filter
			level.ParentID = &parentID
			children, err := s.SearchIssues(c, "", level)
			if err != nil {
				t.Fatalf("SearchIssues(ParentID=%s): %v", parentID, err)
			}
			for _, child := range children {
				if !seen[child.ID] {
					seen[child.ID] = true
					walk(child.ID)
				}
			}
		}
		walk("dw-r.1")
		ids := make([]string, 0, len(seen))
		for id := range seen {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return ids
	}

	open := types.StatusOpen
	for _, tc := range []struct {
		name   string
		filter types.IssueFilter
		want   []string
	}{
		{"no filter", types.IssueFilter{}, []string{"dw-a", "dw-a1", "dw-c", "dw-c1", "dw-r", "dw-r.1", "dw-r.1.1", "dw-w", "dw-w1"}},
		{"status open", types.IssueFilter{Status: &open}, []string{"dw-a", "dw-a1", "dw-r", "dw-r.1", "dw-r.1.1", "dw-w", "dw-w1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ref := perNode(t, tc.filter); !slices.Equal(ref, tc.want) {
				t.Fatalf("per-node SearchIssues walk = %v, want %v (fixture no longer shaped as documented)", ref, tc.want)
			}
			got, err := storage.SearchDescendants(c, s, "dw-r.1", tc.filter)
			if err != nil {
				t.Fatalf("SearchDescendants: %v", err)
			}
			if ids := issueIDs(got); !slices.Equal(ids, tc.want) {
				t.Fatalf("SearchDescendants = %v, want the per-node walk's %v", ids, tc.want)
			}
		})
	}
}
