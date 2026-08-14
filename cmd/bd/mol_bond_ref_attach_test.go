package main

import (
	"testing"

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

func TestBuildAttachCloneOptsConditionalRefArm(t *testing.T) {
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}

	opts, err := buildAttachCloneOpts(bondSubgraph(), mol, types.BondTypeConditional, nil, "arm-ace", "tester", false, false)
	if err != nil {
		t.Fatalf("buildAttachCloneOpts() error = %v", err)
	}

	if opts.AttachToID != "" {
		t.Errorf("AttachToID = %q, want no blocking attachment for a nested conditional arm", opts.AttachToID)
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
