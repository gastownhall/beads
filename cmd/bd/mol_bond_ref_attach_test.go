package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
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

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeSequential, nil, "arm-ace", "tester", false, false)
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

	_, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeConditional, nil, "arm-ace", "tester", false, false)
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

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeConditional, nil, "", "tester", false, false)
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

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeSequential, nil, "", "tester", false, false)
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

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeParallel, nil, "arm-ace", "tester", false, false)
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
