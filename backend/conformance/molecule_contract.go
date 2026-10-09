package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract for the molecule operations every backend
// serves through one library entry each:
//
//   - CloseRequest.AutoCloseMolecule / CloseBatchRequest.AutoCloseMolecule —
//     the close closes the molecule root it completed IN ITS OWN TRANSACTION
//     (internal/storage/issueops.CloseCompletedMoleculeInTx on the store
//     backends, uow.CloseCompletedMoleculeInUOW on the unit of work, the
//     server's own on http). Opt-in; unforced; guarded on the root revision.
//   - issueops.MoleculeStepper — the advance behind `bd close --continue`,
//     whose claim is a real claim (assignee = Actor) that never takes a step
//     another actor holds.
//
// THE LEAF DOC COMMENTS ARE THE SPEC (issueops/issueops.go CloseRequest,
// issueops/batchcloser.go, issueops/moleculestepper.go, issueops/molecule.go).

// MoleculeFixture supplies adapter-specific storage access for the molecule
// assertions.
type MoleculeFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds.
	IssuePrefix string
	Lifecycle   publicops.Lifecycle
	BatchCloser publicops.BatchCloser
	Stepper     publicops.MoleculeStepper
	// CreateIssue and CreateWisp seed one row in the issues and wisps plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	CreateWisp  func(context.Context, *types.Issue, string) error
	// AddDependency seeds one edge.
	AddDependency func(context.Context, *types.Dependency, string) error
	// QueryScalar runs a single-row query and scans it.
	QueryScalar func(context.Context, string, []any, ...any) error
}

const moleculeActor = "molecule-contract"

type moleculeSeed struct {
	t       *testing.T
	ctx     context.Context
	fixture MoleculeFixture
}

func (s moleculeSeed) issue(id string, typ types.IssueType, status types.Status, ephemeral bool, labels ...string) {
	s.t.Helper()
	issue := &types.Issue{ID: id, Title: "molecule " + id, IssueType: typ, Status: status, Priority: 2, Labels: labels, Ephemeral: ephemeral}
	if status == types.StatusClosed {
		issue.CloseReason = "seeded closed"
	}
	create := s.fixture.CreateIssue
	if ephemeral {
		create = s.fixture.CreateWisp
	}
	if err := create(s.ctx, issue, moleculeActor); err != nil {
		s.t.Fatalf("seed %s: %v", id, err)
	}
}

func (s moleculeSeed) edge(from, to string, typ types.DependencyType) {
	s.t.Helper()
	if err := s.fixture.AddDependency(s.ctx, &types.Dependency{IssueID: from, DependsOnID: to, Type: typ}, moleculeActor); err != nil {
		s.t.Fatalf("seed edge %s -> %s: %v", from, to, err)
	}
}

// row reads id's status and assignee from whichever plane holds it.
func (s moleculeSeed) row(id string) (status, assignee string) {
	s.t.Helper()
	err := s.fixture.QueryScalar(s.ctx,
		`SELECT status, COALESCE(assignee, '') FROM issues WHERE id = ?
		 UNION ALL SELECT status, COALESCE(assignee, '') FROM wisps WHERE id = ?`,
		[]any{id, id}, &status, &assignee)
	if err != nil {
		s.t.Fatalf("read %s: %v", id, err)
	}
	return status, assignee
}

// molecule seeds root <- step1 (closed), step2 (open) and answers the ids.
func (s moleculeSeed) molecule(name string, rootType types.IssueType, ephemeral bool, labels ...string) (root, done, last string) {
	s.t.Helper()
	root = s.fixture.IssuePrefix + "-" + name
	done, last = root+".1", root+".2"
	s.issue(root, rootType, types.StatusOpen, ephemeral, labels...)
	s.issue(done, types.TypeTask, types.StatusClosed, ephemeral)
	s.issue(last, types.TypeTask, types.StatusOpen, ephemeral)
	s.edge(done, root, types.DepParentChild)
	s.edge(last, root, types.DepParentChild)
	return root, done, last
}

func (s moleculeSeed) closeStep(id string, autoClose bool) publicops.CloseResult {
	s.t.Helper()
	res, err := s.fixture.Lifecycle.Close(s.ctx, publicops.CloseRequest{
		Actor: moleculeActor, IssueID: id, Reason: "step done", Session: "molecule-session", AutoCloseMolecule: autoClose,
	})
	if err != nil {
		s.t.Fatalf("close %s: %v", id, err)
	}
	return res
}

// RunMoleculeCloseAutoClosesTheCompletedRoot pins CloseRequest.AutoCloseMolecule:
// closing the last open step closes an auto-closing root in the same call,
// reports it, and records the auto-close reason.
func RunMoleculeCloseAutoClosesTheCompletedRoot(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	for _, shape := range []struct {
		name     string
		rootType types.IssueType
		labels   []string
	}{
		{"poured", types.TypeMolecule, nil},
		{"template", types.TypeEpic, []string{publicops.MoleculeTemplateLabel}},
	} {
		root, _, last := s.molecule("auto-"+shape.name, shape.rootType, false, shape.labels...)
		res := s.closeStep(last, true)
		if !res.Changed || res.Issue == nil || res.Issue.Status != types.StatusClosed {
			t.Fatalf("%s: close %s = %+v, want the step closed", shape.name, last, res)
		}
		if res.AutoClosedMolecule == nil || res.AutoClosedMolecule.ID != root {
			t.Fatalf("%s: AutoClosedMolecule = %+v, want %s", shape.name, res.AutoClosedMolecule, root)
		}
		if res.MoleculeAutoCloseRefusal != "" {
			t.Errorf("%s: MoleculeAutoCloseRefusal = %q, want none", shape.name, res.MoleculeAutoCloseRefusal)
		}
		if status, _ := s.row(root); status != string(types.StatusClosed) {
			t.Errorf("%s: root %s status = %s, want closed", shape.name, root, status)
		}
		var reason string
		if err := fixture.QueryScalar(ctx, `SELECT close_reason FROM issues WHERE id = ?`, []any{root}, &reason); err != nil {
			t.Fatalf("read %s close_reason: %v", root, err)
		}
		if reason != publicops.MoleculeAutoCloseReason {
			t.Errorf("%s: root close_reason = %q, want %q", shape.name, reason, publicops.MoleculeAutoCloseReason)
		}
	}
}

// RunMoleculeAutoCloseIsOptInAndRuleBound pins the opt-in default and the
// three roots the auto-close must leave open: one a request did not ask for,
// an incomplete one, and an ordinary epic (explicit close-eligible work).
func RunMoleculeAutoCloseIsOptInAndRuleBound(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}

	root, _, last := s.molecule("optin", types.TypeMolecule, false)
	if res := s.closeStep(last, false); res.AutoClosedMolecule != nil {
		t.Errorf("close without AutoCloseMolecule reported %s auto-closed", res.AutoClosedMolecule.ID)
	}
	if status, _ := s.row(root); status != string(types.StatusOpen) {
		t.Errorf("root %s status after an un-flagged close = %s, want open", root, status)
	}

	root, _, last = s.molecule("plain", types.TypeEpic, false)
	if res := s.closeStep(last, true); res.AutoClosedMolecule != nil {
		t.Errorf("an ordinary epic auto-closed: %s", res.AutoClosedMolecule.ID)
	}
	if status, _ := s.row(root); status != string(types.StatusOpen) {
		t.Errorf("ordinary epic %s status = %s, want open", root, status)
	}

	root, done, last := s.molecule("incomplete", types.TypeMolecule, false)
	if res := s.closeStep(done, true); res.AutoClosedMolecule != nil {
		t.Errorf("an incomplete molecule auto-closed: %s", res.AutoClosedMolecule.ID)
	}
	if status, _ := s.row(root); status != string(types.StatusOpen) {
		t.Errorf("incomplete molecule %s status = %s, want open (%s still open)", root, status, last)
	}
}

// RunMoleculeRecloseHealsAStrandedRoot pins the replay clause: an idempotent
// re-close of the last step still closes a root left open.
func RunMoleculeRecloseHealsAStrandedRoot(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	root := fixture.IssuePrefix + "-stranded"
	step := root + ".1"
	s.issue(root, types.TypeMolecule, types.StatusOpen, false)
	s.issue(step, types.TypeTask, types.StatusClosed, false)
	s.edge(step, root, types.DepParentChild)

	res := s.closeStep(step, true)
	if res.Changed {
		t.Errorf("re-close of %s reported Changed", step)
	}
	if res.AutoClosedMolecule == nil || res.AutoClosedMolecule.ID != root {
		t.Fatalf("re-close AutoClosedMolecule = %+v, want %s healed", res.AutoClosedMolecule, root)
	}
	if status, _ := s.row(root); status != string(types.StatusClosed) {
		t.Errorf("stranded root %s status = %s, want closed", root, status)
	}
}

// RunMoleculeBatchCloseAutoClosesTheRootOnce pins the batch clause: a batch
// that closes the remaining steps closes the root once, on one outcome.
func RunMoleculeBatchCloseAutoClosesTheRootOnce(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	root, _, last := s.molecule("batch", types.TypeMolecule, false)
	other := root + ".3"
	s.issue(other, types.TypeTask, types.StatusOpen, false)
	s.edge(other, root, types.DepParentChild)

	res, err := fixture.BatchCloser.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:             moleculeActor,
		Items:             []publicops.BatchCloseItem{{IssueID: last, Reason: "done"}, {IssueID: other, Reason: "done"}},
		AutoCloseMolecule: true,
	})
	if err != nil {
		t.Fatalf("CloseBatch: %v", err)
	}
	reported := 0
	for _, outcome := range res.Outcomes {
		if outcome.Err != nil {
			t.Fatalf("outcome %s: %v", outcome.IssueID, outcome.Err)
		}
		if outcome.AutoClosedMolecule != nil {
			reported++
			if outcome.AutoClosedMolecule.ID != root {
				t.Errorf("outcome %s auto-closed %s, want %s", outcome.IssueID, outcome.AutoClosedMolecule.ID, root)
			}
		}
	}
	if reported != 1 {
		t.Errorf("the root was reported on %d outcomes, want exactly one", reported)
	}
	if status, _ := s.row(root); status != string(types.StatusClosed) {
		t.Errorf("root %s status = %s, want closed", root, status)
	}
}

// RunMoleculeRefusedRootKeepsTheStepClose pins the refusal clause: a root its
// own close policy refuses (a live blocker) stays open and is reported, and
// the step close it followed stands.
func RunMoleculeRefusedRootKeepsTheStepClose(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	root, _, last := s.molecule("blocked", types.TypeMolecule, false)
	blocker := root + "-blocker"
	s.issue(blocker, types.TypeTask, types.StatusOpen, false)
	s.edge(root, blocker, types.DepBlocks)

	res := s.closeStep(last, true)
	if res.AutoClosedMolecule != nil {
		t.Errorf("a blocked root auto-closed: %s", res.AutoClosedMolecule.ID)
	}
	if res.MoleculeAutoCloseRefusal == "" {
		t.Error("MoleculeAutoCloseRefusal is empty; a refused root must be reported")
	}
	if status, _ := s.row(last); status != string(types.StatusClosed) {
		t.Errorf("step %s status = %s, want closed (the refusal must not undo it)", last, status)
	}
	if status, _ := s.row(root); status != string(types.StatusOpen) {
		t.Errorf("blocked root %s status = %s, want open", root, status)
	}
}

// RunMoleculeStepperClaimsTheNextReadyStepForTheActor pins the advance and its
// claim: the next ready step becomes in progress AND assigned to Actor; without
// AutoClaim nothing is written.
func RunMoleculeStepperClaimsTheNextReadyStepForTheActor(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	root := fixture.IssuePrefix + "-advance"
	a, b, c := root+".1", root+".2", root+".3"
	s.issue(root, types.TypeMolecule, types.StatusOpen, false)
	s.issue(a, types.TypeTask, types.StatusClosed, false)
	s.issue(b, types.TypeTask, types.StatusOpen, false)
	s.issue(c, types.TypeTask, types.StatusOpen, false)
	for _, id := range []string{a, b, c} {
		s.edge(id, root, types.DepParentChild)
	}
	s.edge(c, b, types.DepBlocks)

	res, err := fixture.Stepper.Advance(ctx, publicops.AdvanceRequest{Actor: moleculeActor, ClosedStepID: a})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if res.MoleculeID != root || res.Claimed || res.NextStep == nil || res.NextStep.ID != b {
		t.Fatalf("Advance without a claim = %+v, want next %s in %s, unclaimed", res, b, root)
	}
	if status, assignee := s.row(b); status != string(types.StatusOpen) || assignee != "" {
		t.Errorf("%s after an unclaimed advance = %s/%q, want open and unassigned", b, status, assignee)
	}

	res, err = fixture.Stepper.Advance(ctx, publicops.AdvanceRequest{Actor: moleculeActor, ClosedStepID: a, AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance with a claim: %v", err)
	}
	if !res.Claimed || res.NextStep == nil || res.NextStep.ID != b || res.NextStep.Assignee != moleculeActor {
		t.Fatalf("Advance with a claim = %+v, want %s claimed for %s", res, b, moleculeActor)
	}
	if status, assignee := s.row(b); status != string(types.StatusInProgress) || assignee != moleculeActor {
		t.Errorf("%s after the claim = %s/%q, want in_progress assigned to %s", b, status, assignee, moleculeActor)
	}
	if status, _ := s.row(c); status != string(types.StatusOpen) {
		t.Errorf("%s (blocked) status = %s, want open", c, status)
	}
}

// RunMoleculeStepperNeverTakesAStepAnotherActorHolds pins the refusal the claim
// carries: a ready step assigned to someone else is skipped for the next one,
// and left exactly as it was.
func RunMoleculeStepperNeverTakesAStepAnotherActorHolds(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	root := fixture.IssuePrefix + "-held"
	a, held, free := root+".1", root+".2", root+".3"
	s.issue(root, types.TypeMolecule, types.StatusOpen, false)
	s.issue(a, types.TypeTask, types.StatusClosed, false)
	heldIssue := &types.Issue{ID: held, Title: "held", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2, Assignee: "someone-else"}
	if err := fixture.CreateIssue(ctx, heldIssue, moleculeActor); err != nil {
		t.Fatalf("seed %s: %v", held, err)
	}
	s.issue(free, types.TypeTask, types.StatusOpen, false)
	for _, id := range []string{a, held, free} {
		s.edge(id, root, types.DepParentChild)
	}

	res, err := fixture.Stepper.Advance(ctx, publicops.AdvanceRequest{Actor: moleculeActor, ClosedStepID: a, AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !res.Claimed || res.NextStep == nil || res.NextStep.ID != free {
		t.Fatalf("Advance = %+v, want %s skipped (held) and %s claimed", res, held, free)
	}
	if status, assignee := s.row(held); status != string(types.StatusOpen) || assignee != "someone-else" {
		t.Errorf("held step %s = %s/%q, want untouched (open, someone-else)", held, status, assignee)
	}
}

// RunMoleculeStepperAnswersCompleteAndOutsideAMolecule pins the two answers
// that claim nothing.
func RunMoleculeStepperAnswersCompleteAndOutsideAMolecule(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	orphan := fixture.IssuePrefix + "-orphan"
	s.issue(orphan, types.TypeTask, types.StatusClosed, false)
	res, err := fixture.Stepper.Advance(ctx, publicops.AdvanceRequest{Actor: moleculeActor, ClosedStepID: orphan, AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance(orphan): %v", err)
	}
	if res.MoleculeID != "" || res.NextStep != nil || res.Claimed || res.ClosedStep == nil || res.ClosedStep.ID != orphan {
		t.Errorf("Advance(orphan) = %+v, want the step and no molecule", res)
	}

	root := fixture.IssuePrefix + "-complete"
	s.issue(root, types.TypeEpic, types.StatusOpen, false)
	s.issue(root+".1", types.TypeTask, types.StatusClosed, false)
	s.edge(root+".1", root, types.DepParentChild)
	res, err = fixture.Stepper.Advance(ctx, publicops.AdvanceRequest{Actor: moleculeActor, ClosedStepID: root + ".1", AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance(complete): %v", err)
	}
	if res.MoleculeID != root || !res.Complete || res.NextStep != nil || res.Claimed {
		t.Errorf("Advance(complete) = %+v, want %s complete", res, root)
	}

	if _, err := fixture.Stepper.Advance(ctx, publicops.AdvanceRequest{Actor: moleculeActor, ClosedStepID: fixture.IssuePrefix + "-absent"}); !errors.Is(err, publicops.ErrNotFound) {
		t.Errorf("Advance(absent) = %v, want ErrNotFound", err)
	}
}

// RunMoleculeStepperClaimsAWispStep pins the wisp plane: a wisp molecule's next
// step is claimed for the actor too.
func RunMoleculeStepperClaimsAWispStep(t *testing.T, ctx context.Context, fixture MoleculeFixture) {
	t.Helper()
	s := moleculeSeed{t, ctx, fixture}
	root, done, last := s.molecule("wisp", types.TypeMolecule, true)
	res, err := fixture.Stepper.Advance(ctx, publicops.AdvanceRequest{Actor: moleculeActor, ClosedStepID: done, AutoClaim: true})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if res.MoleculeID != root || !res.Claimed || res.NextStep == nil || res.NextStep.ID != last {
		t.Fatalf("Advance over a wisp molecule = %+v, want %s claimed", res, last)
	}
	if status, assignee := s.row(last); status != string(types.StatusInProgress) || assignee != moleculeActor {
		t.Errorf("wisp step %s = %s/%q, want in_progress assigned to %s", last, status, assignee, moleculeActor)
	}
}
