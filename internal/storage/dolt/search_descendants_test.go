package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchDescendantsMatchesPerNodeWalk pins the storage.DescendantSearcher
// capability the --parent tree walk routes through (GH#3415): the whole tree
// in one read transaction must answer the same ID set the per-node
// SearchIssues walk — the recursive walk this capability replaced — answered,
// across every child shape that walk had to survive: edge children, a wisp
// child (its edge lives in wisp_dependencies), an edge-less dotted-id child
// (matched by the id LIKE branch), a cycle back to the root (the walk must
// converge, not loop), and a closed mid-level child with its own subtree
// (which a Status filter must be able to prune).
func TestSearchDescendantsMatchesPerNodeWalk(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const (
		rootID   = "desc-tree-root"
		aID      = "desc-tree-a"
		a1ID     = "desc-tree-a1"
		cycID    = "desc-tree-cyc"
		dottedID = "desc-tree-root.dotted"
		closedID = "desc-tree-closed"
		ccID     = "desc-tree-cc"
	)

	newIssue := func(id string, status types.Status) *types.Issue {
		return &types.Issue{ID: id, Title: "descendant fixture " + id, Status: status, IssueType: types.TypeTask}
	}
	create := func(issue *types.Issue) {
		t.Helper()
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	parentEdge := func(child, parent string) {
		t.Helper()
		dep := &types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild}
		if err := store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", child, parent, err)
		}
	}

	create(newIssue(rootID, types.StatusOpen))
	create(newIssue(aID, types.StatusOpen))
	parentEdge(aID, rootID)
	create(newIssue(a1ID, types.StatusOpen))
	parentEdge(a1ID, aID)
	create(newIssue(cycID, types.StatusOpen))
	parentEdge(cycID, a1ID)

	// The cycle edge: the root becomes a child of its own grandchild. The
	// one-shot AddDependencyWithOptions drops SkipCycleCheck (only the
	// transaction API forwards it), and the walk's contract is convergence on
	// already-broken data, so plant the edge inside a transaction, where the
	// per-edge cycle probe is the caller's to skip.
	err := store.RunInTransaction(ctx, "test: seed descendant-walk cycle edge", func(tx storage.Transaction) error {
		return tx.AddDependencyWithOptions(ctx,
			&types.Dependency{IssueID: rootID, DependsOnID: cycID, Type: types.DepParentChild},
			"tester", storage.DependencyAddOptions{SkipCycleCheck: true})
	})
	if err != nil {
		t.Fatalf("seed cycle edge %s -> %s: %v", rootID, cycID, err)
	}

	// A wisp child: its parent edge routes to wisp_dependencies because the
	// source is ephemeral.
	wisp := &types.Issue{Title: "descendant fixture wisp", Status: types.StatusOpen, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue (wisp): %v", err)
	}
	if wisp.ID == "" {
		wisps, err := store.SearchIssues(ctx, "descendant fixture wisp", types.IssueFilter{Ephemeral: ptr(true)})
		if err != nil || len(wisps) == 0 {
			t.Fatalf("wisp ID unassigned and lookup failed: %v (results %d)", err, len(wisps))
		}
		wisp.ID = wisps[0].ID
	}
	parentEdge(wisp.ID, rootID)

	// An edge-less child whose id alone makes it the root's: matched by the
	// dotted-id LIKE branch, never by the edge subquery.
	create(newIssue(dottedID, types.StatusOpen))

	// A closed mid-level child with its own child.
	create(newIssue(closedID, types.StatusOpen))
	parentEdge(closedID, rootID)
	create(newIssue(ccID, types.StatusOpen))
	parentEdge(ccID, closedID)
	if err := store.CloseIssue(ctx, closedID, "done", "tester", "s1"); err != nil {
		t.Fatalf("CloseIssue %s: %v", closedID, err)
	}

	// perNodeWalk is the reference implementation: the recursive per-node
	// SearchIssues walk findAllDescendants ran before the capability.
	perNodeWalk := func(filter types.IssueFilter) []string {
		t.Helper()
		var out []string
		seen := make(map[string]bool)
		var visit func(node string)
		visit = func(node string) {
			nodeFilter := filter
			nodeFilter.ParentID = &node
			nodeFilter.Limit = 0
			children, err := store.SearchIssues(ctx, "", nodeFilter)
			if err != nil {
				t.Fatalf("per-node walk under %s: %v", node, err)
			}
			for _, child := range children {
				if seen[child.ID] {
					continue
				}
				seen[child.ID] = true
				out = append(out, child.ID)
				visit(child.ID)
			}
		}
		visit(rootID)
		return out
	}
	ids := func(issues []*types.Issue) []string {
		out := make([]string, len(issues))
		for i, issue := range issues {
			out[i] = issue.ID
		}
		return out
	}

	// Unfiltered: every shape is present, and the cycle converges — the root
	// itself comes back (it is now a child of its own grandchild), exactly as
	// the per-node walk always answered it.
	got, err := store.SearchDescendants(ctx, rootID, types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchDescendants: %v", err)
	}
	if want := []string{rootID, aID, a1ID, cycID, wisp.ID, dottedID, closedID, ccID}; !equalStringSlices(ids(got), want) {
		t.Fatalf("SearchDescendants IDs = %v, want %v", ids(got), want)
	}
	if walk := perNodeWalk(types.IssueFilter{}); !equalStringSlices(ids(got), walk) {
		t.Fatalf("SearchDescendants IDs = %v, per-node walk = %v", ids(got), walk)
	}

	// Status: open prunes the closed child AND the subtree only reachable
	// through it, on both the capability and the walk.
	openGot, err := store.SearchDescendants(ctx, rootID, types.IssueFilter{Status: ptr(types.StatusOpen)})
	if err != nil {
		t.Fatalf("SearchDescendants (open): %v", err)
	}
	if want := []string{rootID, aID, a1ID, cycID, wisp.ID, dottedID}; !equalStringSlices(ids(openGot), want) {
		t.Fatalf("SearchDescendants (open) IDs = %v, want %v", ids(openGot), want)
	}
	if walk := perNodeWalk(types.IssueFilter{Status: ptr(types.StatusOpen)}); !equalStringSlices(ids(openGot), walk) {
		t.Fatalf("SearchDescendants (open) IDs = %v, per-node walk (open) = %v", ids(openGot), walk)
	}
}
