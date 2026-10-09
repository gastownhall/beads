package issueops_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeMolecules is an in-memory MoleculeReader, Reader (List), Claimer and
// Lifecycle (Update with Claim, Close) — just enough store for the molecule
// rules, so each rule is pinned without a database.
type fakeMolecules struct {
	issueops.Reader
	issueops.Lifecycle
	issues map[string]*types.Issue
	deps   []*types.Dependency

	edgeErr    error
	listErr    error
	relatedErr error
	// claimErrs answers a claim of the keyed id with that error.
	claimErrs map[string]error
	claimed   []string
	updated   []string
	closes    []issueops.CloseRequest
	closeErr  error
}

func newFakeMolecules() *fakeMolecules {
	return &fakeMolecules{issues: map[string]*types.Issue{}, claimErrs: map[string]error{}}
}

func (f *fakeMolecules) add(id string, typ types.IssueType, status types.Status, labels ...string) *types.Issue {
	issue := &types.Issue{ID: id, Title: "title " + id, IssueType: typ, Status: status, Labels: labels, RowVersion: int64(len(id) * 7)}
	f.issues[id] = issue
	return issue
}

func (f *fakeMolecules) edge(from, to string, typ types.DependencyType) {
	f.deps = append(f.deps, &types.Dependency{IssueID: from, DependsOnID: to, Type: typ})
}

func (f *fakeMolecules) GetMany(_ context.Context, req issueops.GetManyRequest) (issueops.GetManyResult, error) {
	out := issueops.GetManyResult{Issues: []*issueops.Issue{}, Missing: []string{}}
	for _, id := range req.IDs {
		if issue := f.issues[id]; issue != nil {
			clone := *issue
			out.Issues = append(out.Issues, &clone)
		} else {
			out.Missing = append(out.Missing, id)
		}
	}
	return out, nil
}

func (f *fakeMolecules) Related(_ context.Context, req issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	if f.relatedErr != nil {
		return nil, f.relatedErr
	}
	if f.issues[req.ID] == nil {
		return nil, fmt.Errorf("%w: %s", issueops.ErrNotFound, req.ID)
	}
	var out []*issueops.RelatedIssue
	for _, dep := range f.deps {
		if dep.DependsOnID != req.ID || (len(req.Types) > 0 && !slices.Contains(req.Types, dep.Type)) {
			continue
		}
		if issue := f.issues[dep.IssueID]; issue != nil {
			out = append(out, &issueops.RelatedIssue{Issue: *issue, DependencyType: dep.Type})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeMolecules) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	if f.edgeErr != nil {
		return issueops.EdgeReadResult{}, f.edgeErr
	}
	var out issueops.EdgeReadResult
	for _, id := range req.IDs {
		anchor := issueops.AnchorEdges{ID: id, Missing: f.issues[id] == nil}
		for _, dep := range f.deps {
			if dep.IssueID == id && (len(req.Types) == 0 || slices.Contains(req.Types, dep.Type)) {
				anchor.Edges = append(anchor.Edges, dep)
			}
		}
		out.Anchors = append(out.Anchors, anchor)
	}
	return out, nil
}

func (f *fakeMolecules) List(_ context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	if f.listErr != nil {
		return issueops.IssuePage{}, f.listErr
	}
	var page issueops.IssuePage
	for _, issue := range f.issues {
		if string(issue.Status) == req.Status && (req.Assignee == "" || issue.Assignee == req.Assignee) {
			page.Items = append(page.Items, &types.IssueWithCounts{Issue: issue})
		}
	}
	sort.Slice(page.Items, func(i, j int) bool { return page.Items[i].ID < page.Items[j].ID })
	return page, nil
}

func (f *fakeMolecules) take(id, actor string) (*types.Issue, error) {
	if err := f.claimErrs[id]; err != nil {
		return nil, err
	}
	issue := f.issues[id]
	issue.Status, issue.Assignee = types.StatusInProgress, actor
	clone := *issue
	clone.Labels = nil
	return &clone, nil
}

func (f *fakeMolecules) Claim(_ context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	issue, err := f.take(req.IssueID, req.Actor)
	if err != nil {
		return issueops.ClaimResult{}, err
	}
	f.claimed = append(f.claimed, req.IssueID)
	return issueops.ClaimResult{Issue: issue, Changed: true}, nil
}

func (f *fakeMolecules) Update(_ context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	if !req.Claim {
		return issueops.UpdateResult{}, errors.New("fake: only claims")
	}
	issue, err := f.take(req.IssueID, req.Actor)
	if err != nil {
		return issueops.UpdateResult{}, err
	}
	f.updated = append(f.updated, req.IssueID)
	return issueops.UpdateResult{Issue: issue, Changed: true}, nil
}

func (f *fakeMolecules) Close(_ context.Context, req issueops.CloseRequest) (issueops.CloseResult, error) {
	f.closes = append(f.closes, req)
	if f.closeErr != nil {
		return issueops.CloseResult{}, f.closeErr
	}
	issue := f.issues[req.IssueID]
	if req.ExpectedVersion != nil && *req.ExpectedVersion != issue.RowVersion {
		return issueops.CloseResult{}, issueops.ErrVersionMismatch
	}
	issue.Status = types.StatusClosed
	clone := *issue
	return issueops.CloseResult{Issue: &clone, Changed: true}, nil
}

func (f *fakeMolecules) reader() issueops.MoleculeReader { return issueops.MoleculeReaderOver(f) }

func (f *fakeMolecules) stepper() issueops.MoleculeStepper {
	return issueops.NewMoleculeStepper(issueops.MoleculeStepperRoles{Reader: f.reader(), Claimer: f, Lifecycle: f})
}

// A molecule: root (molecule type) <- a, b, c; c blocked by b; b blocked by a.
func seedChain(f *fakeMolecules) {
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.a", types.TypeTask, types.StatusClosed)
	f.add("m.b", types.TypeTask, types.StatusOpen)
	f.add("m.c", types.TypeTask, types.StatusOpen)
	for _, id := range []string{"m.a", "m.b", "m.c"} {
		f.edge(id, "m", types.DepParentChild)
	}
	f.edge("m.b", "m.a", types.DepBlocks)
	f.edge("m.c", "m.b", types.DepBlocks)
}

func TestMoleculeRootsWalksToTheTopmostMoleculeRoot(t *testing.T) {
	f := newFakeMolecules()
	f.add("root", types.TypeEpic, types.StatusOpen)
	f.add("child", types.TypeTask, types.StatusOpen)
	f.add("grandchild", types.TypeTask, types.StatusOpen)
	f.add("orphan", types.TypeTask, types.StatusOpen)
	f.add("plain", types.TypeTask, types.StatusOpen)
	f.add("plainkid", types.TypeTask, types.StatusOpen)
	f.add("tmpl", types.TypeTask, types.StatusOpen, issueops.MoleculeTemplateLabel)
	f.add("tmplkid", types.TypeTask, types.StatusOpen)
	f.edge("child", "root", types.DepParentChild)
	f.edge("grandchild", "child", types.DepParentChild)
	f.edge("plainkid", "plain", types.DepParentChild)
	f.edge("tmplkid", "tmpl", types.DepParentChild)

	roots, err := issueops.MoleculeRoots(context.Background(), f.reader(), []string{"grandchild", "child", "root", "orphan", "plainkid", "tmplkid", "absent"})
	if err != nil {
		t.Fatalf("MoleculeRoots: %v", err)
	}
	want := map[string]string{"grandchild": "root", "child": "root", "root": "root", "tmplkid": "tmpl"}
	if len(roots) != len(want) {
		t.Fatalf("MoleculeRoots = %v, want %v", roots, want)
	}
	for id, root := range want {
		if roots[id] != root {
			t.Errorf("MoleculeRoots[%s] = %q, want %q", id, roots[id], root)
		}
	}
}

// Every read the rules make RETURNS a refusal. Answering "not in a molecule"
// or "no molecules" for a read that never happened is how `bd mol current`
// went silently empty and a molecule root was stranded open.
func TestMoleculeReadsReturnRefusals(t *testing.T) {
	ctx := context.Background()
	refusal := errors.New("refused")

	f := newFakeMolecules()
	seedChain(f)
	f.edgeErr = refusal
	if _, err := issueops.MoleculeRoots(ctx, f.reader(), []string{"m.b"}); !errors.Is(err, refusal) {
		t.Errorf("MoleculeRoots with a refused edge read = %v, want the refusal", err)
	}
	if _, err := issueops.CompletedMolecule(ctx, f.reader(), "m.b"); !errors.Is(err, refusal) {
		t.Errorf("CompletedMolecule with a refused edge read = %v, want the refusal", err)
	}

	f = newFakeMolecules()
	seedChain(f)
	f.relatedErr = refusal
	if _, err := issueops.ViewMolecule(ctx, f.reader(), "m"); !errors.Is(err, refusal) {
		t.Errorf("ViewMolecule with a refused relations read = %v, want the refusal", err)
	}
	if _, err := issueops.ReadMoleculeProgress(ctx, f.reader(), "m"); !errors.Is(err, refusal) {
		t.Errorf("ReadMoleculeProgress with a refused relations read = %v, want the refusal", err)
	}

	f = newFakeMolecules()
	f.listErr = refusal
	if _, err := issueops.ActiveMoleculeIDs(ctx, f, f.reader(), "alice"); !errors.Is(err, refusal) {
		t.Errorf("ActiveMoleculeIDs with a refused listing = %v, want the refusal", err)
	}
	if _, err := issueops.InProgressMoleculeIDs(ctx, f, f.reader(), "alice"); !errors.Is(err, refusal) {
		t.Errorf("InProgressMoleculeIDs with a refused listing = %v, want the refusal", err)
	}
}

func TestMoleculeViewDerivesStatesProgressAndTheNextStep(t *testing.T) {
	f := newFakeMolecules()
	seedChain(f)
	f.add("m.d", types.TypeTask, types.StatusInProgress)
	f.edge("m.d", "m", types.DepParentChild)
	f.issues["m.d"].Assignee = "alice"

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	if view.Total != 4 || view.Completed != 1 || view.Complete() {
		t.Errorf("progress = %d/%d complete=%v, want 1/4 incomplete", view.Completed, view.Total, view.Complete())
	}
	states := map[string]string{}
	for _, step := range view.Steps {
		states[step.Issue.ID] = step.State
	}
	want := map[string]string{"m.a": "done", "m.b": "ready", "m.c": "pending", "m.d": "current"}
	for id, state := range want {
		if states[id] != state {
			t.Errorf("state[%s] = %q, want %q", id, states[id], state)
		}
	}
	if view.CurrentStep == nil || view.CurrentStep.ID != "m.d" || view.CurrentStep.Assignee != "alice" {
		t.Errorf("CurrentStep = %+v, want m.d assigned to alice (the full step row)", view.CurrentStep)
	}
	if view.NextStep == nil || view.NextStep.ID != "m.b" {
		t.Errorf("NextStep = %+v, want m.b", view.NextStep)
	}
	// Ready is ready --mol's list: every analysis-ready node, hydrated.
	ready := map[string]*issueops.Issue{}
	for _, issue := range view.Ready {
		ready[issue.ID] = issue
	}
	if ready["m.b"] == nil || ready["m.d"] == nil || ready["m.a"] != nil || ready["m.c"] != nil {
		t.Errorf("Ready = %v, want m.b and m.d, never the closed m.a or the blocked m.c", view.Ready)
	}
	if d := ready["m.d"]; d != nil && d.Assignee != "alice" {
		t.Errorf("Ready m.d assignee = %q, want alice (the full step row)", d.Assignee)
	}
	// Ordered by in-molecule blockers, fewest first: m.c waits on one step.
	if last := view.Steps[len(view.Steps)-1].Issue.ID; last != "m.c" && last != "m.b" {
		t.Errorf("last step = %s, want a step that waits on a blocker", last)
	}
	progress, err := issueops.ReadMoleculeProgress(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ReadMoleculeProgress: %v", err)
	}
	if progress.Total != 4 || progress.Completed != 1 || progress.InProgress != 1 || progress.CurrentStepID != "m.d" {
		t.Errorf("progress = %+v, want 4 total, 1 completed, 1 in progress (m.d)", progress)
	}
}

func TestCompletedMoleculeAnswersOnlyAnOpenCompleteAutoClosingRoot(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		rootType types.IssueType
		labels   []string
		closeAll bool
		want     bool
	}{
		{"molecule root, all steps closed", types.TypeMolecule, nil, true, true},
		{"template epic, all steps closed", types.TypeEpic, []string{issueops.MoleculeTemplateLabel}, true, true},
		{"plain epic stays open for explicit close", types.TypeEpic, nil, true, false},
		{"a step still open", types.TypeMolecule, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMolecules()
			f.add("r", tc.rootType, types.StatusOpen, tc.labels...)
			f.add("r.1", types.TypeTask, types.StatusClosed)
			status := types.StatusOpen
			if tc.closeAll {
				status = types.StatusClosed
			}
			f.add("r.2", types.TypeTask, status)
			f.edge("r.1", "r", types.DepParentChild)
			f.edge("r.2", "r", types.DepParentChild)
			root, err := issueops.CompletedMolecule(ctx, f.reader(), "r.1")
			if err != nil {
				t.Fatalf("CompletedMolecule: %v", err)
			}
			if (root != nil) != tc.want {
				t.Fatalf("CompletedMolecule = %v, want completed=%v", root, tc.want)
			}
		})
	}

	f := newFakeMolecules()
	f.add("orphan", types.TypeTask, types.StatusClosed)
	if root, err := issueops.CompletedMolecule(ctx, f.reader(), "orphan"); err != nil || root != nil {
		t.Errorf("CompletedMolecule(orphan) = %v, %v, want nil, nil", root, err)
	}
}

func TestMoleculeRootCloseIsUnforcedAndGuardedOnTheRootRevision(t *testing.T) {
	root := &issueops.Issue{ID: "r", RowVersion: 41}
	got := issueops.MoleculeRootClose(root, "alice", "sess")
	if got.ExpectedVersion == nil || *got.ExpectedVersion != 41 || got.Force {
		t.Errorf("root close = %+v, want unforced and guarded on the root revision 41", got)
	}
	if got.IssueID != "r" || got.Reason != issueops.MoleculeAutoCloseReason || got.Session != "sess" || got.Actor != "alice" {
		t.Errorf("root close = %+v, want root r, the auto-close reason, the session and the actor", got)
	}
	root.RowVersion = 42
	if *got.ExpectedVersion != 41 {
		t.Error("the guard aliases the root's revision; it must be the revision read")
	}
}

func TestMoleculeStepperClaimsTheNextReadyStepForTheActor(t *testing.T) {
	ctx := context.Background()
	f := newFakeMolecules()
	seedChain(f)

	// Without AutoClaim nothing is written.
	res, err := f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "m.a"})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if res.MoleculeID != "m" || res.Claimed || res.NextStep == nil || res.NextStep.ID != "m.b" || len(f.claimed) != 0 {
		t.Fatalf("Advance without a claim = %+v (claimed %v), want next m.b and no claim", res, f.claimed)
	}

	res, err = f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "m.a", AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !res.Claimed || res.NextStep == nil || res.NextStep.ID != "m.b" || res.NextStep.Assignee != "alice" ||
		res.NextStep.Status != types.StatusInProgress {
		t.Fatalf("Advance = %+v, want m.b claimed for alice", res)
	}
	if !slices.Equal(f.claimed, []string{"m.b"}) || len(f.updated) != 0 {
		t.Errorf("claims = %v / updates = %v, want one Claimer claim of m.b", f.claimed, f.updated)
	}
}

// A ready step another actor holds is skipped, never taken; any other claim
// failure is returned rather than read as a lost race.
func TestMoleculeStepperSkipsAHeldStepAndReturnsOtherFailures(t *testing.T) {
	ctx := context.Background()
	f := newFakeMolecules()
	f.add("m", types.TypeMolecule, types.StatusOpen)
	for _, id := range []string{"m.1", "m.2"} {
		f.add(id, types.TypeTask, types.StatusOpen)
		f.edge(id, "m", types.DepParentChild)
	}
	f.add("m.0", types.TypeTask, types.StatusClosed)
	f.edge("m.0", "m", types.DepParentChild)
	f.claimErrs["m.1"] = &issueops.ClaimConflictError{Err: issueops.ErrAlreadyClaimed}

	res, err := f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "m.0", AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !res.Claimed || res.NextStep.ID != "m.2" {
		t.Fatalf("Advance = %+v, want m.1 skipped (held) and m.2 claimed", res)
	}

	f.issues["m.2"].Status, f.issues["m.2"].Assignee = types.StatusOpen, ""
	boom := errors.New("connection reset")
	f.claimErrs["m.1"] = boom
	if _, err := f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "m.0", AutoClaim: true}); !errors.Is(err, boom) {
		t.Fatalf("Advance with a failed claim = %v, want the failure", err)
	}

	f.claimErrs["m.1"] = issueops.ErrNotClaimable
	f.claimErrs["m.2"] = issueops.ErrAlreadyClaimed
	res, err = f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "m.0", AutoClaim: true})
	if err != nil || res.Claimed || res.NextStep == nil || res.NextStep.ID != "m.1" {
		t.Fatalf("Advance with every ready step lost = %+v, %v; want no claim and the first ready step named", res, err)
	}
}

func TestMoleculeStepperClaimsAWispStepThroughLifecycle(t *testing.T) {
	f := newFakeMolecules()
	f.add("w", types.TypeMolecule, types.StatusOpen).Ephemeral = true
	f.add("w.1", types.TypeTask, types.StatusClosed).Ephemeral = true
	f.add("w.2", types.TypeTask, types.StatusOpen).Ephemeral = true
	f.edge("w.1", "w", types.DepParentChild)
	f.edge("w.2", "w", types.DepParentChild)
	res, err := f.stepper().Advance(context.Background(), issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "w.1", AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !res.Claimed || !slices.Equal(f.updated, []string{"w.2"}) || len(f.claimed) != 0 {
		t.Fatalf("Advance over a wisp = %+v (updates %v, claims %v), want w.2 claimed through Lifecycle", res, f.updated, f.claimed)
	}
}

func TestMoleculeStepperOutsideAMoleculeAndWhenComplete(t *testing.T) {
	ctx := context.Background()
	f := newFakeMolecules()
	f.add("orphan", types.TypeTask, types.StatusClosed)
	res, err := f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "orphan", AutoClaim: true})
	if err != nil || res.MoleculeID != "" || res.ClosedStep == nil || res.NextStep != nil {
		t.Fatalf("Advance outside a molecule = %+v, %v; want no molecule", res, err)
	}

	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.1", types.TypeTask, types.StatusClosed)
	f.edge("m.1", "m", types.DepParentChild)
	res, err = f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "m.1", AutoClaim: true})
	if err != nil || !res.Complete || res.NextStep != nil {
		t.Fatalf("Advance on a complete molecule = %+v, %v; want complete", res, err)
	}

	if _, err := f.stepper().Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "absent"}); !errors.Is(err, issueops.ErrNotFound) {
		t.Errorf("Advance on an absent step = %v, want ErrNotFound", err)
	}
	for _, req := range []issueops.AdvanceRequest{{ClosedStepID: "m.1"}, {Actor: "alice"}} {
		if _, err := f.stepper().Advance(ctx, req); !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("Advance(%+v) = %v, want ErrValidation", req, err)
		}
	}
}

func TestActiveMoleculeIDsFallsBackToHookedWork(t *testing.T) {
	ctx := context.Background()
	f := newFakeMolecules()
	seedChain(f)
	f.issues["m.b"].Status, f.issues["m.b"].Assignee = types.StatusInProgress, "alice"
	ids, err := issueops.ActiveMoleculeIDs(ctx, f, f.reader(), "alice")
	if err != nil || !slices.Equal(ids, []string{"m"}) {
		t.Fatalf("ActiveMoleculeIDs = %v, %v, want [m]", ids, err)
	}

	g := newFakeMolecules()
	g.add("epic", types.TypeEpic, types.StatusOpen)
	g.add("handoff", types.TypeTask, types.StatusHooked).Assignee = "bob"
	g.edge("handoff", "epic", types.DepBlocks)
	ids, err = issueops.ActiveMoleculeIDs(ctx, g, g.reader(), "bob")
	if err != nil || !slices.Equal(ids, []string{"epic"}) {
		t.Fatalf("ActiveMoleculeIDs (hooked) = %v, %v, want [epic]", ids, err)
	}
	ids, err = issueops.ActiveMoleculeIDs(ctx, g, g.reader(), "carol")
	if err != nil || len(ids) != 0 {
		t.Fatalf("ActiveMoleculeIDs (nobody's work) = %v, %v, want none", ids, err)
	}
}
