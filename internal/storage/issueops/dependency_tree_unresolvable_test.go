package issueops

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bd-uschhs (#5850) AC-6: the dependency-tree walk recurses into whatever
// GetDependenciesWithMetadataInTx hands it, and GetIssueInTx on an id with
// no row fails the WHOLE walk with not-found — so a traversal that received
// a missing target would not just draw a wrong tree, it would refuse to draw
// one at all. The metadata reader's issue-only contract (its drops are the
// load-bearing seam; see hydrateAnchorEdgesInTx) is what protects the
// recursion. This test drives the full walk over an anchor with one real and
// one unresolvable target: the hydration batch carries both far ids, answers
// with a row for the real one only, and the walk must then descend into the
// real child alone — no fetch of the missing id, no error, no placeholder
// node. sqlmock fails the test on any query the walk was not given an
// expectation for, so a regression that hands the traversal the raw stored
// edge ids turns this red twice over: the unexpected GetIssueInTx for the
// missing id, and the not-found abort of the walk.
func TestGetDependencyTreeInTxNeverFetchesAnUnresolvableTarget(t *testing.T) {
	t.Parallel()

	const root = "ud-1"
	const child = "ud-2"
	const external = "external:liveop:ghost"

	_, mock, tx := beginMockTx(t)
	// The root probe.
	expectIssue(mock, root, "Root")
	// The root's edges: one real child (blocks), one unresolvable target
	// (tracks). Both reach the hydration batch; only the child hydrates.
	expectOutgoingAnchorEdges(mock, root,
		[]anchorEdgeRow{{child, "blocks"}, {external, "tracks"}}, nil)
	expectEdgeHydration(mock, []string{child, external}, map[string]string{child: "Child"})
	// The recursion descends into the child ALONE: its issue probe and its
	// (empty) edge read. No expectation exists for the external id — an
	// attempt to fetch it fails the walk with an unexpected query.
	expectIssue(mock, child, "Child")
	expectOutgoingAnchorEdges(mock, child, nil, nil)

	tree, err := GetDependencyTreeInTx(context.Background(), tx, root, 3, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTreeInTx: %v (the walk must not fail on an edge whose target has no row)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}

	// And the answer is the two real nodes, in pre-order, with the real
	// child carrying the edge type — and nothing else: no stub node for
	// the external target, no marker row.
	if len(tree) != 2 {
		t.Fatalf("tree has %d nodes, want 2 (root + the real child): %+v", len(tree), tree)
	}
	if tree[0].ID != root || tree[0].Depth != 0 {
		t.Fatalf("tree[0] = %+v, want the root at depth 0", tree[0])
	}
	if tree[1].ID != child || tree[1].Depth != 1 || tree[1].ParentID != root ||
		tree[1].EdgeFromParent != types.DepBlocks {
		t.Fatalf("tree[1] = %+v, want the real child under a blocks edge", tree[1])
	}
}
