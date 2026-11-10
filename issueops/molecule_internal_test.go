package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestBlockingDepths pins the depth rule the parallel grouping keys on: 0 with
// no holding blocker, one more than the deepest holding blocker otherwise. The
// caller classifies the edges (AnalyzeMolecule does, by target posture), so a
// released edge leaves the set here and no longer deepens what it blocked.
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

	// A blocker released by classification (the caller drops it from the
	// set, as AnalyzeMolecule does for a closed blocks target) no longer
	// deepens what it blocked.
	delete(blockedBy["step2"], "step1")
	depths = blockingDepths(g, blockedBy)
	if depths["step2"] != 0 {
		t.Errorf("step2 depth with its blocker released = %d, want 0", depths["step2"])
	}
}

// conditionalMoleculeGraph builds root -> stepA (precondition) with fallback
// stepB bound to stepA over a conditional-blocks edge, so the close-reason
// cases below share one shape.
func conditionalMoleculeGraph(status types.Status, closeReason string) (*MoleculeGraph, *types.Issue, *types.Issue) {
	root := &types.Issue{ID: "mol", Status: types.StatusOpen}
	stepA := &types.Issue{ID: "mol.a", Title: "Precondition", Status: status, CloseReason: closeReason}
	stepB := &types.Issue{ID: "mol.b", Title: "Fallback", Status: types.StatusOpen}
	g := &MoleculeGraph{
		Root:     root,
		Issues:   []*types.Issue{root, stepA, stepB},
		IssueMap: map[string]*types.Issue{"mol": root, "mol.a": stepA, "mol.b": stepB},
		Dependencies: []*types.Dependency{
			{IssueID: "mol.a", DependsOnID: "mol", Type: types.DepParentChild},
			{IssueID: "mol.b", DependsOnID: "mol", Type: types.DepParentChild},
			{IssueID: "mol.b", DependsOnID: "mol.a", Type: types.DepConditionalBlocks},
		},
	}
	return g, stepA, stepB
}

// TestAnalyzeMoleculeConditionalCloseHolding pins how the readiness analysis
// classifies a conditional-blocks edge by its target's close reason
// (GH#5577): only a FAILURE close releases the fallback step. A success close
// keeps the step blocked — the closed precondition still counts in BlockedBy
// and in the blocking depth — while a blocks edge onto the same closed target
// releases as it always has.
func TestAnalyzeMoleculeConditionalCloseHolding(t *testing.T) {
	t.Run("success close still holds the fallback", func(t *testing.T) {
		g, _, stepB := conditionalMoleculeGraph(types.StatusClosed, "completed successfully")
		analysis := AnalyzeMolecule(g)
		info := analysis.Steps[stepB.ID]
		if info.IsReady {
			t.Error("expected the fallback step to stay blocked after a success close of its conditional target")
		}
		if len(info.BlockedBy) != 1 || info.BlockedBy[0] != "mol.a" {
			t.Errorf("BlockedBy = %v, want the closed precondition [mol.a]", info.BlockedBy)
		}
		depths := blockingDepths(g, analysisBlockers(analysis))
		if depths[stepB.ID] != 1 {
			t.Errorf("depth of the held fallback = %d, want 1", depths[stepB.ID])
		}
	})
	t.Run("failure close releases the fallback", func(t *testing.T) {
		g, _, stepB := conditionalMoleculeGraph(types.StatusClosed, "failed: flaky in CI")
		analysis := AnalyzeMolecule(g)
		info := analysis.Steps[stepB.ID]
		if !info.IsReady {
			t.Error("expected the fallback step to be ready after a failure close of its conditional target")
		}
		if len(info.BlockedBy) != 0 {
			t.Errorf("BlockedBy = %v, want none after the failure close", info.BlockedBy)
		}
	})
	t.Run("pinned conditional target releases", func(t *testing.T) {
		g, _, stepB := conditionalMoleculeGraph(types.StatusPinned, "")
		analysis := AnalyzeMolecule(g)
		info := analysis.Steps[stepB.ID]
		if !info.IsReady {
			t.Error("expected the fallback step to be ready while its conditional target is pinned")
		}
	})
	t.Run("closed blocks target releases as before", func(t *testing.T) {
		g, _, stepB := conditionalMoleculeGraph(types.StatusClosed, "completed successfully")
		g.Dependencies[2].Type = types.DepBlocks
		analysis := AnalyzeMolecule(g)
		info := analysis.Steps[stepB.ID]
		if !info.IsReady {
			t.Error("expected the step to be ready after its blocks target closed, unchanged by GH#5577")
		}
		if len(info.BlockedBy) != 0 {
			t.Errorf("BlockedBy = %v, want none after the blocks target closed", info.BlockedBy)
		}
	})
}

// analysisBlockers rebuilds the blockedBy view AnalyzeMolecule classified,
// including the held success-closed conditional edges, so depth checks read
// the same set the analysis did.
func analysisBlockers(analysis *MoleculeAnalysis) map[string]map[string]bool {
	blockedBy := make(map[string]map[string]bool)
	for id, info := range analysis.Steps {
		set := make(map[string]bool, len(info.BlockedBy))
		for _, blocker := range info.BlockedBy {
			set[blocker] = true
		}
		blockedBy[id] = set
	}
	return blockedBy
}
