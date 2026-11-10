package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func bondSubgraph() *TemplateSubgraph {
	root := &types.Issue{ID: "proto-arm", Title: "arm"}
	return &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}
}

// A --ref arm is nested under the molecule, so it must not also be blocked by
// it: the arm would wait for the molecule to close while the molecule waits for
// its own open arm, and bd ready goes empty with nothing to point at.
func TestBuildAttachCloneOptsRefArmIsNotBlockedByItsParent(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeSequential, nil, "arm-ace", nil, "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if opts.AttachToID != "" {
		t.Errorf("AttachToID = %q, want no blocking attachment for a nested arm", opts.AttachToID)
	}
	if opts.ParentID != mol.ID {
		t.Errorf("ParentID = %q, want %q", opts.ParentID, mol.ID)
	}
	if opts.ChildRef != "arm-ace" {
		t.Errorf("ChildRef = %q, want %q", opts.ChildRef, "arm-ace")
	}
}

// A conditional --ref arm is refused rather than degraded. Dropping a
// "conditional-blocks" edge does not lose ordering, it inverts the meaning:
// the edge says "run only if the molecule fails", so an arm without it runs
// unconditionally. A silent false dispatch is worse than the deadlock the
// sequential case trades away, and there is no safe reading of the two flags
// together, so bd refuses the combination instead of guessing.
func TestBuildAttachCloneOptsConditionalRefArmIsRefused(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	_, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeConditional, nil, "arm-ace", nil, "tester", false, false)
	if err == nil {
		t.Fatal("buildAttachCloneOpts() error = nil, want --ref + --type conditional to be refused")
	}
	if !strings.Contains(err.Error(), "--ref cannot be combined with --type conditional") {
		t.Errorf("error = %q, want it to name the refused flag combination", err.Error())
	}
}

// A conditional bond WITHOUT --ref keeps its edge: a sibling arm can wait on
// the molecule, so there is nothing unsatisfiable to work around.
func TestBuildAttachCloneOptsConditionalSiblingKeepsItsEdge(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeConditional, nil, "", nil, "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if opts.AttachToID != mol.ID {
		t.Errorf("AttachToID = %q, want %q", opts.AttachToID, mol.ID)
	}
	if opts.AttachDepType != types.DepConditionalBlocks {
		t.Errorf("AttachDepType = %q, want %q", opts.AttachDepType, types.DepConditionalBlocks)
	}
}

// The refusal also has to fire before either bond route runs, so --dry-run
// and the proxied path refuse identically rather than previewing a bond the
// real command rejects.
func TestGatherMolBondInputRefusesConditionalRefArm(t *testing.T) {
	cmd := &cobra.Command{Use: "bond"}
	registerMolBondFlags(cmd)
	if err := cmd.Flags().Set("type", types.BondTypeConditional); err != nil {
		t.Fatalf("set --type: %v", err)
	}
	if err := cmd.Flags().Set("ref", "arm-ace"); err != nil {
		t.Fatalf("set --ref: %v", err)
	}

	_, err := gatherMolBondInput(cmd, []string{"mol-arm", "bd-patrol"})
	if err == nil {
		t.Fatal("gatherMolBondInput() error = nil, want --ref + --type conditional to be refused")
	}
	if !strings.Contains(err.Error(), "--ref cannot be combined with --type conditional") {
		t.Errorf("error = %q, want it to name the refused flag combination", err.Error())
	}
}

// Without --ref the arm is a sibling, which is satisfiable: the molecule closes
// and then the arm unblocks. That attachment must survive.
func TestBuildAttachCloneOptsSiblingKeepsBlockingAttachment(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeSequential, nil, "", nil, "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if opts.AttachToID != mol.ID {
		t.Errorf("AttachToID = %q, want %q", opts.AttachToID, mol.ID)
	}
	if opts.AttachDepType != types.DepBlocks {
		t.Errorf("AttachDepType = %q, want %q", opts.AttachDepType, types.DepBlocks)
	}
	if opts.ParentID != "" {
		t.Errorf("ParentID = %q, want empty without --ref", opts.ParentID)
	}
}

// A parallel bond never blocked, so nesting changes nothing about it.
func TestBuildAttachCloneOptsParallelRefArmKeepsAttachment(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeParallel, nil, "arm-ace", nil, "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if opts.AttachToID != mol.ID {
		t.Errorf("AttachToID = %q, want %q", opts.AttachToID, mol.ID)
	}
	if opts.AttachDepType != types.DepParentChild {
		t.Errorf("AttachDepType = %q, want %q", opts.AttachDepType, types.DepParentChild)
	}
}

// A sequential --ref arm carries its ordering onto the parent's terminal
// steps, one blocks edge each, and never onto the parent itself.
func TestBuildAttachCloneOptsSequentialRefArmWaitsOnTerminalSteps(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeSequential, nil, "arm-ace", []string{"bd-patrol.2", "bd-patrol.3"}, "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if !slices.Equal(opts.AttachBlocksOn, []string{"bd-patrol.2", "bd-patrol.3"}) {
		t.Errorf("AttachBlocksOn = %v, want the resolved terminal steps [bd-patrol.2 bd-patrol.3]", opts.AttachBlocksOn)
	}
	if opts.AttachToID != "" {
		t.Errorf("AttachToID = %q, want no blocking attachment for a nested arm", opts.AttachToID)
	}
}

// A parent with no terminal steps bonds the arm with no ordering edges at
// all - not with an edge onto the container the empty snapshot replaced.
func TestBuildAttachCloneOptsSequentialRefArmEmptyTerminalsOrderNothing(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeSequential, nil, "arm-ace", nil, "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if len(opts.AttachBlocksOn) != 0 {
		t.Errorf("AttachBlocksOn = %v, want none for an empty terminal snapshot", opts.AttachBlocksOn)
	}
	if opts.AttachToID != "" {
		t.Errorf("AttachToID = %q, want no fallback edge onto the container", opts.AttachToID)
	}
}

// A parallel --ref arm is never ordered, so terminal steps resolved for it
// are ignored rather than attached.
func TestBuildAttachCloneOptsParallelRefArmIgnoresTerminalSteps(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeParallel, nil, "arm-ace", []string{"bd-patrol.2"}, "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if len(opts.AttachBlocksOn) != 0 {
		t.Errorf("AttachBlocksOn = %v, want none: a parallel arm is never ordered", opts.AttachBlocksOn)
	}
}

// refReadsStub fails every role read, standing in for a store the molecule
// read cannot reach.
type refReadsStub struct{ err error }

func (s refReadsStub) GetMany(context.Context, issueops.GetManyRequest) (issueops.GetManyResult, error) {
	return issueops.GetManyResult{}, s.err
}

func (s refReadsStub) Related(context.Context, issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	return nil, s.err
}

func (s refReadsStub) ReadEdges(context.Context, issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	return issueops.EdgeReadResult{}, s.err
}

// A failed molecule read fails the bond, naming the molecule it could not
// read - an arm ordered on a guess would silently run too early.
func TestRefArmAfterStepsSurfacesReadFailure(t *testing.T) {
	readErr := errors.New("db unreachable")
	_, err := refArmAfterSteps(context.Background(), issueops.MoleculeReaderOver(refReadsStub{err: readErr}), types.BondTypeSequential, "arm-ace", "bd-patrol")
	if err == nil {
		t.Fatal("refArmAfterSteps() error = nil, want the read failure")
	}
	if !strings.Contains(err.Error(), "bd-patrol") {
		t.Errorf("error = %q, want it to name the molecule it could not read", err.Error())
	}
	if !errors.Is(err, readErr) {
		t.Errorf("error = %q, want it to wrap %v", err.Error(), readErr)
	}
}

// Shapes that never order - no --ref, or a non-sequential bond - answer nil
// without touching the reader at all, so a caller that only constructs one
// lazily never constructs it for them.
func TestRefArmAfterStepsLeavesUnorderedShapesAlone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bondType string
		childRef string
	}{
		{"no_ref", types.BondTypeSequential, ""},
		{"parallel_ref", types.BondTypeParallel, "arm-ace"},
		{"conditional_ref", types.BondTypeConditional, "arm-ace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := refArmAfterSteps(context.Background(), issueops.MoleculeReader{}, tc.bondType, tc.childRef, "bd-patrol")
			if err != nil {
				t.Fatalf("refArmAfterSteps() error = %v", err)
			}
			if got != nil {
				t.Errorf("refArmAfterSteps() = %v, want nil: %s never orders an arm", got, tc.name)
			}
		})
	}
}
