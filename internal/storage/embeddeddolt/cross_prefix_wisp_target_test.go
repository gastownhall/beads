//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCrossPrefixWispTargetBlocksLikeALocalWisp pins the wisp-target
// half of GH#8299: an active wisp row wins classification whatever its id
// prefix, so an edge pointing at it occupies depends_on_wisp_id — for an
// issue source (dependencies) and a wisp source (wisp_dependencies) alike —
// and its blocked-state effect is the same as an equivalent same-prefix wisp
// dependency. On base the prefix rule filed these edges external and the
// cross-prefix sources never saw their wisp blockers.
func TestEmbeddedCrossPrefixWispTargetBlocksLikeALocalWisp(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	seed := []*types.Issue{
		// issue sources, one per comparison side
		{ID: "a-src", Title: "issue source, cross-prefix target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "a-src2", Title: "issue source, same-prefix target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		// wisp sources, one per comparison side
		{ID: "a-wsrc", Title: "wisp source, cross-prefix target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "a-wsrc2", Title: "wisp source, same-prefix target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		// wisp targets: cross-prefix active wisps and their same-prefix twins
		{ID: "b-w1", Title: "wisp blocker, cross prefix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "b-w2", Title: "wisp blocker, cross prefix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "a-w3", Title: "wisp blocker, same prefix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "a-w4", Title: "wisp blocker, same prefix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	}
	for _, issue := range seed {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	addBlocks := func(source, target string) {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: source, DependsOnID: target, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", source, target, err)
		}
	}

	// Both source tiers route a foreign-prefix active wisp into
	// depends_on_wisp_id.
	addBlocks("a-src", "b-w1")
	assertDepTargetColumn(t, te, "dependencies", "a-src", "b-w1", "depends_on_wisp_id")
	addBlocks("a-wsrc", "b-w2")
	assertDepTargetColumn(t, te, "wisp_dependencies", "a-wsrc", "b-w2", "depends_on_wisp_id")

	// The equivalent same-prefix wisp dependencies, for the behavior
	// comparison below.
	addBlocks("a-src2", "a-w3")
	addBlocks("a-wsrc2", "a-w4")

	// Blocked-state behavior matches: while the wisp blockers are open, the
	// cross-prefix sources are blocked exactly like their same-prefix twins,
	// on both planes.
	var crossIssue, sameIssue bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{"a-src"}, &crossIssue)
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{"a-src2"}, &sameIssue)
	if !sameIssue {
		t.Fatalf("fixture check: same-prefix wisp dependency left a-src2 unblocked; the comparison twin is not blocking")
	}
	if crossIssue != sameIssue {
		t.Errorf("issue source blocked by cross-prefix wisp = %v, want %v (same as same-prefix wisp dependency)", crossIssue, sameIssue)
	}
	var crossWisp, sameWisp bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM wisps WHERE id = ?", []any{"a-wsrc"}, &crossWisp)
	te.queryScalar(t, ctx, "SELECT is_blocked FROM wisps WHERE id = ?", []any{"a-wsrc2"}, &sameWisp)
	if !sameWisp {
		t.Fatalf("fixture check: same-prefix wisp dependency left a-wsrc2 unblocked; the comparison twin is not blocking")
	}
	if crossWisp != sameWisp {
		t.Errorf("wisp source blocked by cross-prefix wisp = %v, want %v (same as same-prefix wisp dependency)", crossWisp, sameWisp)
	}
}
