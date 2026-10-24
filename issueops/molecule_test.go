package issueops_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	// related records every Related read, by id, in order.
	related []string
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
	f.related = append(f.related, req.ID)
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

// seedGroupedFormula builds the exact grouped-formula shape from GH#7638:
// brief <- group (needs brief) <- a, b (needs a). Only Brief should ever be
// ready: Group is blocked by the still-open Brief, and A is blocked too,
// inherited from its blocked parent Group, even though A carries no direct
// blocking edge of its own.
func seedGroupedFormula(f *fakeMolecules) {
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.brief", types.TypeTask, types.StatusOpen)
	f.add("m.group", types.TypeTask, types.StatusOpen)
	f.add("m.a", types.TypeTask, types.StatusOpen)
	f.add("m.b", types.TypeTask, types.StatusOpen)
	for _, id := range []string{"m.brief", "m.group"} {
		f.edge(id, "m", types.DepParentChild)
	}
	for _, id := range []string{"m.a", "m.b"} {
		f.edge(id, "m.group", types.DepParentChild)
	}
	f.edge("m.group", "m.brief", types.DepBlocks)
	f.edge("m.b", "m.a", types.DepBlocks)
}

// TestMoleculeReadyInheritsParentBlockedState pins GH#7638: bd ready --mol
// must agree with bd ready and bd blocked that a step whose parent is
// blocked is itself blocked (parent-child's documented semantics,
// docs/workflows/molecules.md:111, BlockedStateInvariant), and must never
// offer the molecule root itself as a ready step.
func TestMoleculeReadyInheritsParentBlockedState(t *testing.T) {
	f := newFakeMolecules()
	seedGroupedFormula(f)

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}

	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if want := map[string]bool{"m.brief": true}; !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready = %v, want only m.brief: Group is blocked by the open Brief, A inherits Group's blocked state, B is blocked directly by A, and the root is not a step", ready)
	}

	info := view.Analysis.Steps["m.a"]
	if info == nil || info.IsReady {
		t.Fatalf("Steps[m.a].IsReady = %+v, want not ready: its parent m.group is blocked", info)
	}
	if len(info.BlockedBy) != 0 {
		t.Fatalf("Steps[m.a].BlockedBy = %v, want empty: A carries no direct blocking edge, only an inherited one", info.BlockedBy)
	}
}

// TestMoleculeReadyLetsAChildGoReadyOnceItsClosedParentStopsBlocking confirms
// the inheritance in the test above is not a blanket ban on a parent-child
// edge: once Group (A and B's parent) closes, it is no longer itself
// blocked (closed rows are never blocked), so A becomes ready on its own
// merits again.
func TestMoleculeReadyLetsAChildGoReadyOnceItsClosedParentStopsBlocking(t *testing.T) {
	f := newFakeMolecules()
	seedGroupedFormula(f)
	f.issues["m.brief"].Status = types.StatusClosed
	f.issues["m.group"].Status = types.StatusClosed

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if !ready["m.a"] {
		t.Fatalf("Ready = %v, want m.a ready once its closed parent m.group no longer blocks it", ready)
	}
	if ready["m.b"] {
		t.Fatalf("Ready = %v, want m.b still blocked directly by the still-open m.a", ready)
	}
}

// TestMoleculeReadyEpicCloseGateOnOwnChildrenIsNotLocked pins GH#7652
// Finding 2: a parent-child edge must propagate only EXOGENOUS blockedness
// (internal/storage/issueops/blocked_consistency.go's #6506 contract), not
// whatever set parentInfo.BlockedBy. The root here blocks on its own
// children — the "close gate on the epic" idiom — so neither child's
// blocker is outside the root's own subtree. Before this fix that still
// read as blocked (len(BlockedBy) > 0 regardless of target), so both
// children locked forever: nothing could ever close, so the gate could
// never open.
func TestMoleculeReadyEpicCloseGateOnOwnChildrenIsNotLocked(t *testing.T) {
	f := newFakeMolecules()
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.a", types.TypeTask, types.StatusOpen)
	f.add("m.b", types.TypeTask, types.StatusOpen)
	f.edge("m.a", "m", types.DepParentChild)
	f.edge("m.b", "m", types.DepParentChild)
	f.edge("m", "m.a", types.DepBlocks) // the root cannot close before its own children
	f.edge("m", "m.b", types.DepBlocks)

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if want := map[string]bool{"m.a": true, "m.b": true}; !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready = %v, want %v: the root's own close-gate blockers are inside its own subtree, so they must not darken the children it is waiting for", ready, want)
	}
}

// TestMoleculeReadyParentBlockedBySiblingStillLocksChild is the mirror of
// the test above: the parent's blocker is a true sibling, OUTSIDE the
// parent's subtree, so the exogenous-only narrowing above must not weaken
// the ordinary inheritance pinned by TestMoleculeReadyInheritsParentBlockedState.
func TestMoleculeReadyParentBlockedBySiblingStillLocksChild(t *testing.T) {
	f := newFakeMolecules()
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.p", types.TypeTask, types.StatusOpen)
	f.add("m.c", types.TypeTask, types.StatusOpen)
	f.add("m.x", types.TypeTask, types.StatusOpen)
	f.edge("m.p", "m", types.DepParentChild)
	f.edge("m.x", "m", types.DepParentChild)
	f.edge("m.c", "m.p", types.DepParentChild)
	f.edge("m.p", "m.x", types.DepBlocks) // p's blocker x is a sibling, not p's own descendant

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if want := map[string]bool{"m.x": true}; !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready = %v, want only m.x: p is exogenously blocked by sibling x, so p's own child c must still inherit the block", ready)
	}
}

// TestMoleculeReadyChildWithTwoParentsIsBlockedIfEitherParentIs pins GH#7652
// Finding 3: a child under two parent-child parents got a nondeterministic
// answer, because childParent kept only one parent per child, chosen by map
// iteration order. childParents now keeps every parent, and isAncestorBlocked
// treats "any blocked parent" as blocking — which is deterministic by
// construction, independent of map order.
func TestMoleculeReadyChildWithTwoParentsIsBlockedIfEitherParentIs(t *testing.T) {
	f := newFakeMolecules()
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.p1", types.TypeTask, types.StatusOpen)
	f.add("m.p2", types.TypeTask, types.StatusOpen)
	f.add("m.c", types.TypeTask, types.StatusOpen)
	f.add("m.x", types.TypeTask, types.StatusOpen)
	f.edge("m.p1", "m", types.DepParentChild)
	f.edge("m.p2", "m", types.DepParentChild)
	f.edge("m.x", "m", types.DepParentChild)
	// m.c has TWO parent-child parents: p1 (exogenously blocked by sibling x)
	// and p2 (clear).
	f.edge("m.c", "m.p1", types.DepParentChild)
	f.edge("m.c", "m.p2", types.DepParentChild)
	f.edge("m.p1", "m.x", types.DepBlocks)

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if ready["m.c"] {
		t.Fatalf("Ready = %v, want m.c NOT ready: one of its two parents (p1) is blocked", ready)
	}
	if !ready["m.x"] {
		t.Fatalf("Ready = %v, want m.x ready: it carries no blocker of its own", ready)
	}
}

// TestMoleculeReadyParentChildCycleDoesNotCrash pins GH#7652 Finding 4: a
// parent-child cycle (arriving one edge at a time, or through import/merge,
// per the consistency tests' own note) must not overflow the stack. a and b
// are each other's parent-child parent, and each is also a plain
// parent-child child of the root, so the cycle is reachable from m without
// otherwise affecting readiness (neither carries a real blocker).
func TestMoleculeReadyParentChildCycleDoesNotCrash(t *testing.T) {
	f := newFakeMolecules()
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.a", types.TypeTask, types.StatusOpen)
	f.add("m.b", types.TypeTask, types.StatusOpen)
	f.edge("m.a", "m", types.DepParentChild)
	f.edge("m.b", "m", types.DepParentChild)
	f.edge("m.a", "m.b", types.DepParentChild) // a's parents: m, b
	f.edge("m.b", "m.a", types.DepParentChild) // b's parents: m, a — the cycle

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if want := map[string]bool{"m.a": true, "m.b": true}; !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready = %v, want %v: the cycle carries no blocker, so it must not read as blocked either", ready, want)
	}
}

// TestMoleculeReadyClosedMiddleParentStopsInheritedBlock pins GH#7652
// Finding 5(a): the closed/pinned-parent exemption. A three-level chain
// where the grandparent is genuinely exogenously blocked, but the middle
// parent between it and the leaf is closed — inheritance must stop at the
// closed parent, exactly as TestMoleculeReadyLetsAChildGoReadyOnceItsClosedParentStopsBlocking
// pins one level up.
func TestMoleculeReadyClosedMiddleParentStopsInheritedBlock(t *testing.T) {
	f := newFakeMolecules()
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.g", types.TypeTask, types.StatusOpen)
	f.add("m.mid", types.TypeTask, types.StatusClosed)
	f.add("m.leaf", types.TypeTask, types.StatusOpen)
	f.add("m.x", types.TypeTask, types.StatusOpen)
	f.edge("m.g", "m", types.DepParentChild)
	f.edge("m.x", "m", types.DepParentChild)
	f.edge("m.mid", "m.g", types.DepParentChild)
	f.edge("m.leaf", "m.mid", types.DepParentChild)
	f.edge("m.g", "m.x", types.DepBlocks) // g is exogenously blocked by sibling x

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if !ready["m.leaf"] {
		t.Fatalf("Ready = %v, want m.leaf ready: its closed parent m.mid stops the blocked-grandparent chain from inheriting past it", ready)
	}
}

// TestMoleculeReadyGrandparentBlockInheritsThroughAnUnblockedMiddleParent
// pins GH#7652 Finding 5(b): the `|| isParentExogenouslyBlocked(grandParentID)`
// transitivity clause. The middle parent here carries no blocker of its
// own, so without the clause the leaf would read as ready even though its
// grandparent is genuinely exogenously blocked.
func TestMoleculeReadyGrandparentBlockInheritsThroughAnUnblockedMiddleParent(t *testing.T) {
	f := newFakeMolecules()
	f.add("m", types.TypeMolecule, types.StatusOpen)
	f.add("m.g", types.TypeTask, types.StatusOpen)
	f.add("m.mid", types.TypeTask, types.StatusOpen)
	f.add("m.leaf", types.TypeTask, types.StatusOpen)
	f.add("m.x", types.TypeTask, types.StatusOpen)
	f.edge("m.g", "m", types.DepParentChild)
	f.edge("m.x", "m", types.DepParentChild)
	f.edge("m.mid", "m.g", types.DepParentChild)
	f.edge("m.leaf", "m.mid", types.DepParentChild)
	f.edge("m.g", "m.x", types.DepBlocks) // g is exogenously blocked by sibling x; mid carries no blocker of its own

	view, err := issueops.ViewMolecule(context.Background(), f.reader(), "m")
	if err != nil {
		t.Fatalf("ViewMolecule: %v", err)
	}
	ready := map[string]bool{}
	for _, issue := range view.Ready {
		ready[issue.ID] = true
	}
	if ready["m.leaf"] {
		t.Fatalf("Ready = %v, want m.leaf NOT ready: m.g's exogenous block must inherit transitively through m.mid even though m.mid carries no direct blocker", ready)
	}
	if ready["m.mid"] {
		t.Fatalf("Ready = %v, want m.mid NOT ready either: it inherits m.g's block directly", ready)
	}
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

// A close under a root that cannot auto-close — an ordinary epic, a root
// already closed — reads no steps at all, and an eligible root's steps are read
// only up to the first open one.
func TestCompletedMoleculeReadsOnlyTheStepsItMust(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		rootType types.IssueType
		status   types.Status
	}{
		{"ordinary epic", types.TypeEpic, types.StatusOpen},
		{"closed molecule root", types.TypeMolecule, types.StatusClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMolecules()
			f.add("r", tc.rootType, tc.status)
			for _, id := range []string{"r.1", "r.2"} {
				f.add(id, types.TypeTask, types.StatusClosed)
				f.edge(id, "r", types.DepParentChild)
			}
			root, err := issueops.CompletedMolecule(ctx, f.reader(), "r.1")
			if err != nil || root != nil {
				t.Fatalf("CompletedMolecule = %v, %v, want nil, nil", root, err)
			}
			if len(f.related) != 0 {
				t.Errorf("steps read %v, want none: this root never auto-closes", f.related)
			}
		})
	}

	// r.1 is open; r.2 and r.3 are closed and have steps of their own. The
	// walk answers at r.1 without reading below r.2 or r.3.
	f := newFakeMolecules()
	f.add("r", types.TypeMolecule, types.StatusOpen)
	f.add("r.1", types.TypeTask, types.StatusOpen)
	f.edge("r.1", "r", types.DepParentChild)
	for _, id := range []string{"r.2", "r.3"} {
		f.add(id, types.TypeTask, types.StatusClosed)
		f.edge(id, "r", types.DepParentChild)
		f.add(id+".1", types.TypeTask, types.StatusClosed)
		f.edge(id+".1", id, types.DepParentChild)
	}
	root, err := issueops.CompletedMolecule(ctx, f.reader(), "r.2")
	if err != nil || root != nil {
		t.Fatalf("CompletedMolecule = %v, %v, want nil, nil (r.1 is open)", root, err)
	}
	if !slices.Equal(f.related, []string{"r"}) {
		t.Errorf("steps read %v, want only r's: r.1 is open, so nothing below r is read", f.related)
	}
}

// MoleculeStepsClosed covers every descendant, not just the direct steps, and a
// parent-child cycle terminates.
func TestMoleculeStepsClosedCoversEveryDescendant(t *testing.T) {
	ctx := context.Background()
	f := newFakeMolecules()
	f.add("r", types.TypeMolecule, types.StatusOpen)
	f.add("r.1", types.TypeTask, types.StatusClosed)
	f.edge("r.1", "r", types.DepParentChild)
	f.add("r.1.1", types.TypeTask, types.StatusOpen)
	f.edge("r.1.1", "r.1", types.DepParentChild)
	if closed, err := issueops.MoleculeStepsClosed(ctx, f.reader(), "r"); err != nil || closed {
		t.Errorf("MoleculeStepsClosed with r.1.1 open = %v, %v, want false", closed, err)
	}

	f.issues["r.1.1"].Status = types.StatusClosed
	f.edge("r", "r.1.1", types.DepParentChild) // a cycle back to the root
	f.related = nil
	if closed, err := issueops.MoleculeStepsClosed(ctx, f.reader(), "r"); err != nil || !closed {
		t.Errorf("MoleculeStepsClosed with every step closed = %v, %v, want true", closed, err)
	}
	if len(f.related) != 3 {
		t.Errorf("steps read %v, want r, r.1 and r.1.1 once each", f.related)
	}

	f.relatedErr = errors.New("refused")
	if _, err := issueops.MoleculeStepsClosed(ctx, f.reader(), "r"); !errors.Is(err, f.relatedErr) {
		t.Errorf("MoleculeStepsClosed with a refused read = %v, want the refusal", err)
	}
}

// The root close's refusals that leave the step's close standing are the close
// policy and the close guards; a lost race on the root's revision is not one.
func TestIsMoleculeAutoCloseRefusalIsPolicyAndGuardsNeverAVersionRace(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"live blocker", &issueops.BlockedError{IssueID: "r", Blockers: []issueops.Blocker{{ID: "b"}}, Err: issueops.ErrCloseBlocked}, true},
		{"open children", &issueops.CloseOpenChildrenError{IssueID: "r", OpenChildren: 1}, true},
		{"template root", &issueops.TemplateReadOnlyError{IssueID: "r"}, true},
		{"pinned root", &issueops.PinnedError{IssueID: "r"}, true},
		{"root assigned to someone else", &issueops.CloseNotAssigneeError{IssueID: "r", Assignee: "bob", Actor: "alice"}, true},
		{"wrapped guard", fmt.Errorf("close r: %w", &issueops.CloseNotAssigneeError{IssueID: "r", Assignee: "bob", Actor: "alice"}), true},
		{"lost race on the root revision", issueops.ErrVersionMismatch, false},
		{"typed lost race", &issueops.VersionMismatchError{Expected: 1, Current: 2}, false},
		{"wrapped lost race", fmt.Errorf("close r: %w", &issueops.VersionMismatchError{Expected: 1, Current: 2}), false},
		{"storage failure", errors.New("connection reset"), false},
		{"no error", nil, false},
	} {
		if got := issueops.IsMoleculeAutoCloseRefusal(tc.err); got != tc.want {
			t.Errorf("%s: IsMoleculeAutoCloseRefusal(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
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
