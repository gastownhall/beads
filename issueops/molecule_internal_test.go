package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestBlockingDepths pins the depth rule the parallel grouping keys on: 0 with
// no open blocker, one more than the deepest open blocker otherwise.
func TestBlockingDepths(t *testing.T) {
	root := &types.Issue{ID: "root", Status: types.StatusOpen}
	step1 := &types.Issue{ID: "step1", Status: types.StatusOpen}
	step2 := &types.Issue{ID: "step2", Status: types.StatusOpen}
	step3 := &types.Issue{ID: "step3", Status: types.StatusOpen}
	g := &MoleculeGraph{
		Root:     root,
		Issues:   []*types.Issue{root, step1, step2, step3},
		IssueMap: map[string]*types.Issue{"root": root, "step1": step1, "step2": step2, "step3": step3},
	}
	blockedBy := map[string]map[string]bool{
		"root":  {},
		"step1": {"root": true},
		"step2": {"step1": true},
		"step3": {"step2": true},
	}
	openBlocker := func(_, blockerID string) bool {
		blocker := g.IssueMap[blockerID]
		return blocker != nil && blocker.Status != types.StatusClosed
	}
	depths := blockingDepths(g, blockedBy, openBlocker)
	for id, want := range map[string]int{"root": 0, "step1": 1, "step2": 2, "step3": 3} {
		if depths[id] != want {
			t.Errorf("%s depth = %d, want %d", id, depths[id], want)
		}
	}

	// A closed blocker no longer deepens what it blocked.
	step1.Status = types.StatusClosed
	depths = blockingDepths(g, blockedBy, openBlocker)
	if depths["step2"] != 0 {
		t.Errorf("step2 depth with its blocker closed = %d, want 0", depths["step2"])
	}
}

// TestAnalyzeMoleculeConditionalBlocksFollowsTheCloseReason: a step behind a
// conditional-blocks edge stays held, and one level deeper, while its target is
// closed with a non-failure reason; a failure close releases it, as an ordinary
// blocks edge is released by any close.
func TestAnalyzeMoleculeConditionalBlocksFollowsTheCloseReason(t *testing.T) {
	for _, tc := range []struct {
		depType   types.DependencyType
		reason    string
		wantReady bool
	}{
		{types.DepConditionalBlocks, "completed successfully", false},
		{types.DepConditionalBlocks, "", false},
		{types.DepConditionalBlocks, "failed: upstream 500", true},
		{types.DepBlocks, "completed successfully", true},
	} {
		root := &types.Issue{ID: "root", Status: types.StatusOpen}
		primary := &types.Issue{ID: "primary", Status: types.StatusClosed, CloseReason: tc.reason}
		fallback := &types.Issue{ID: "fallback", Status: types.StatusOpen}
		sibling := &types.Issue{ID: "sibling", Status: types.StatusOpen}
		g := &MoleculeGraph{
			Root:     root,
			Issues:   []*types.Issue{root, primary, fallback, sibling},
			IssueMap: map[string]*types.Issue{"root": root, "primary": primary, "fallback": fallback, "sibling": sibling},
			Dependencies: []*types.Dependency{
				{IssueID: "fallback", DependsOnID: "primary", Type: tc.depType},
			},
		}
		analysis := AnalyzeMolecule(g)
		info := analysis.Steps["fallback"]
		if info.IsReady != tc.wantReady {
			t.Errorf("%s closed %q: fallback ready = %v, want %v (blocked by %v)", tc.depType, tc.reason, info.IsReady, tc.wantReady, info.BlockedBy)
		}
		wantBlockedBy := 0
		if !tc.wantReady {
			wantBlockedBy = 1
		}
		if len(info.BlockedBy) != wantBlockedBy {
			t.Errorf("%s closed %q: fallback blocked by %v, want %d blocker(s)", tc.depType, tc.reason, info.BlockedBy, wantBlockedBy)
		}
		// Parallel depth: a released fallback sits at depth 0 and shares the
		// sibling's group; a held one sits one level deeper, alone.
		sameGroup := info.ParallelGroup != "" && info.ParallelGroup == analysis.Steps["sibling"].ParallelGroup
		if sameGroup != tc.wantReady {
			t.Errorf("%s closed %q: fallback in sibling's parallel group = %v, want %v", tc.depType, tc.reason, sameGroup, tc.wantReady)
		}
	}
}
