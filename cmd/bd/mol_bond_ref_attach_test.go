package main

import (
	"context"
	"errors"
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

// Only a sequential --ref arm loses its edge onto the molecule, so only it
// reads the molecule's terminal steps; every other bond keeps its own
// attachment and must not pick up extra edges.
func TestRefArmNeedsTerminalStepsOnlyForSequentialRef(t *testing.T) {
	cases := []struct {
		bondType, childRef string
		want               bool
	}{
		{types.BondTypeSequential, "arm-ace", true},
		{types.BondTypeSequential, "", false},
		{types.BondTypeParallel, "arm-ace", false},
		{types.BondTypeParallel, "", false},
		{types.BondTypeConditional, "", false},
		{types.BondTypeConditional, "arm-ace", false},
	}
	for _, tc := range cases {
		if got := refArmNeedsTerminalSteps(tc.bondType, tc.childRef); got != tc.want {
			t.Errorf("refArmNeedsTerminalSteps(%q, %q) = %v, want %v", tc.bondType, tc.childRef, got, tc.want)
		}
	}
}

// failingMoleculeReader refuses every read with err.
type failingMoleculeReader struct{ err error }

func (r failingMoleculeReader) GetMany(context.Context, issueops.GetManyRequest) (issueops.GetManyResult, error) {
	return issueops.GetManyResult{}, r.err
}

func (r failingMoleculeReader) Related(context.Context, issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	return nil, r.err
}

func (r failingMoleculeReader) ReadEdges(context.Context, issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	return issueops.EdgeReadResult{}, r.err
}

// refusingMolWriter fails the test on any write: the reads it embeds are
// never reached by the paths that use it.
type refusingMolWriter struct {
	molWriter
	t *testing.T
}

func (w refusingMolWriter) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	w.t.Errorf("CreateIssue(%q) reached after a failed terminal-step read", issue.Title)
	return nil
}

func (w refusingMolWriter) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	w.t.Errorf("AddDependency(%s -> %s) reached after a failed terminal-step read", dep.IssueID, dep.DependsOnID)
	return nil
}

// A terminal-step read that fails is the bond's error, returned before the
// arm is spawned, never an empty target set that would leave the arm
// unordered.
func TestBondProtoMolAttachIntoReturnsATerminalReadFailureBeforeSpawning(t *testing.T) {
	readErr := errors.New("edge read refused")
	mol := &types.Issue{ID: "bd-patrol", Title: "patrol"}
	sub := bondSubgraph()

	_, err := bondProtoMolAttachInto(context.Background(), refusingMolWriter{t: t}, issueops.MoleculeReaderOver(failingMoleculeReader{err: readErr}),
		sub, sub.Root, mol, types.BondTypeSequential, nil, "arm-ace", "tester", false, false)
	if !errors.Is(err, readErr) {
		t.Fatalf("bondProtoMolAttachInto() error = %v, want the read failure %v", err, readErr)
	}
}

// The long help is where the sequential --ref contract is documented (the
// CLI reference is generated from it): wait for every terminal step, read
// once at bond time, ready at once with nothing to wait for, no membership.
func TestMolBondHelpDocumentsRefArmTerminalOrdering(t *testing.T) {
	long := strings.Join(strings.Fields(molBondCmd.Long), " ")
	for _, want := range []string{
		"a sequential --ref arm instead waits on the molecule's terminal steps",
		"The arm runs once ALL of them close.",
		"The set is read once, at bond time: steps added later do not retarget the arm",
		"a molecule with no open steps takes an arm that is ready at once",
		"The arm is not made a member of the molecule",
		"An arm that must run while its molecule is still running, such as a per-worker arm its patrol waits on, needs --type parallel",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("mol bond --help does not say %q", want)
		}
	}
	if strings.Contains(long, "carries no ordering") {
		t.Errorf("mol bond --help still says a sequential --ref arm carries no ordering")
	}
}
