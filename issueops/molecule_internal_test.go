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
	depths := blockingDepths(g, blockedBy)
	for id, want := range map[string]int{"root": 0, "step1": 1, "step2": 2, "step3": 3} {
		if depths[id] != want {
			t.Errorf("%s depth = %d, want %d", id, depths[id], want)
		}
	}

	// A closed blocker no longer deepens what it blocked.
	step1.Status = types.StatusClosed
	depths = blockingDepths(g, blockedBy)
	if depths["step2"] != 0 {
		t.Errorf("step2 depth with its blocker closed = %d, want 0", depths["step2"])
	}
}
