package uow

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestMoleculeContract runs the molecule contract against the unit-of-work
// backend, whose auto-close body (CloseCompletedMoleculeInUOW) is its own and
// whose stepper is composed over this provider's roles. One provider for the
// suite; sequential subtests namespaced by prefix.
func TestMoleculeContract(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "mol")
	fixture := newUOWMoleculeFixture(t, provider, "mol")

	for _, test := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.MoleculeFixture)
	}{
		{"CloseAutoClosesTheCompletedRoot", conformance.RunMoleculeCloseAutoClosesTheCompletedRoot},
		{"AutoCloseIsOptInAndRuleBound", conformance.RunMoleculeAutoCloseIsOptInAndRuleBound},
		{"RecloseHealsAStrandedRoot", conformance.RunMoleculeRecloseHealsAStrandedRoot},
		{"BatchCloseAutoClosesTheRootOnce", conformance.RunMoleculeBatchCloseAutoClosesTheRootOnce},
		{"RefusedRootKeepsTheStepClose", conformance.RunMoleculeRefusedRootKeepsTheStepClose},
		{"GuardedRootKeepsTheStepClose", conformance.RunMoleculeGuardedRootKeepsTheStepClose},
		{"StepperClaimsTheNextReadyStepForTheActor", conformance.RunMoleculeStepperClaimsTheNextReadyStepForTheActor},
		{"StepperNeverTakesAStepAnotherActorHolds", conformance.RunMoleculeStepperNeverTakesAStepAnotherActorHolds},
		{"StepperAnswersCompleteAndOutsideAMolecule", conformance.RunMoleculeStepperAnswersCompleteAndOutsideAMolecule},
		{"StepperClaimsAWispStep", conformance.RunMoleculeStepperClaimsAWispStep},
	} {
		t.Run(test.name, func(t *testing.T) { test.run(t, ctx, fixture) })
	}
}

func newUOWMoleculeFixture(t *testing.T, provider UnitOfWorkProvider, prefix string) conformance.MoleculeFixture {
	t.Helper()
	lifecycle, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	closer, err := NewBatchCloser(provider)
	if err != nil {
		t.Fatalf("NewBatchCloser: %v", err)
	}
	source, ok := provider.(MoleculeStepperSource)
	if !ok {
		t.Fatalf("provider %T does not offer the MoleculeStepper accessor", provider)
	}
	stepper, err := source.MoleculeStepper()
	if err != nil {
		t.Fatalf("MoleculeStepper(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.MoleculeFixture{
		IssuePrefix:   kit.IssuePrefix,
		Lifecycle:     lifecycle,
		BatchCloser:   closer,
		Stepper:       stepper,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
	}
}

// seedRacingMoleculeInUOW seeds an auto-closing molecule root with n open
// steps through kit and answers the steps.
func seedRacingMoleculeInUOW(t *testing.T, ctx context.Context, kit roleFixtureKit, root string, n int) []string {
	t.Helper()
	if err := kit.CreateIssue(ctx, &types.Issue{ID: root, Title: "molecule " + root, IssueType: types.TypeMolecule, Status: types.StatusOpen, Priority: 2}, "tester"); err != nil {
		t.Fatalf("seed %s: %v", root, err)
	}
	steps := make([]string, n)
	for i := range steps {
		steps[i] = fmt.Sprintf("%s.%d", root, i+1)
		if err := kit.CreateIssue(ctx, &types.Issue{ID: steps[i], Title: "step " + steps[i], IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}, "tester"); err != nil {
			t.Fatalf("seed %s: %v", steps[i], err)
		}
		if err := kit.AddDependency(ctx, &types.Dependency{IssueID: steps[i], DependsOnID: root, Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", steps[i], root, err)
		}
	}
	return steps
}

func readStatusInUOW(t *testing.T, ctx context.Context, kit roleFixtureKit, id string) string {
	t.Helper()
	var status string
	if err := kit.QueryScalar(ctx, "SELECT status FROM issues WHERE id = ?", []any{id}, &status); err != nil {
		t.Fatalf("read %s status: %v", id, err)
	}
	return status
}

// closeTwoStepsInRacingUOWs closes steps[0] and steps[1], each followed by
// this backend's auto-close body (CloseCompletedMoleculeInUOW), in two units
// of work that both decide before either commits, so each reads the other's
// step open: the race's interleaving, made deterministic. Neither may
// auto-close the root. It commits both and answers the two commit errors.
func closeTwoStepsInRacingUOWs(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, steps []string) [2]error {
	t.Helper()
	var uws [2]UnitOfWork
	for i := range uws {
		uw, err := provider.NewUOW(ctx)
		if err != nil {
			t.Fatalf("open unit of work %d: %v", i, err)
		}
		defer uw.Close(ctx) // no-op after Commit; preserves cleanup on early Fatal.
		uws[i] = uw
	}
	for i, uw := range uws {
		if _, err := uw.IssueUseCase().CloseIssueChecked(ctx, steps[i], domain.CloseIssueParams{Reason: "done"}, "tester", false); err != nil {
			t.Fatalf("close %s: %v", steps[i], err)
		}
		auto, err := CloseCompletedMoleculeInUOW(ctx, uw, steps[i], "tester", "")
		if err != nil {
			t.Fatalf("auto-close after %s: %v", steps[i], err)
		}
		if auto.Root != nil {
			t.Fatalf("close %s auto-closed %s, but in its own unit of work %s is still open", steps[i], auto.Root.ID, steps[1-i])
		}
	}
	return [2]error{uws[0].Commit(ctx, "close "+steps[0]), uws[1].Commit(ctx, "close "+steps[1])}
}

// TestMoleculeAutoCloseLastStepsCannotBothMissTheRootInUOW is the unit-of-work
// twin of the dolt store's two-transaction regression: two units of work that
// each close one of a molecule's last two open steps and run this backend's
// auto-close body (CloseCompletedMoleculeInUOW) each read the other's step
// open, so neither closes the root. The coordination cell both rewrite must
// make one commit conflict, or the complete molecule's root is stranded open.
func TestMoleculeAutoCloseLastStepsCannotBothMissTheRootInUOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "molrace")
	kit := newUOWRoleFixtureKit(provider, "molrace")
	root := kit.IssuePrefix + "-race"

	commitErrs := closeTwoStepsInRacingUOWs(t, ctx, provider, seedRacingMoleculeInUOW(t, ctx, kit, root, 2))
	t.Logf("last-step commits: %v", commitErrs)
	if commitErrs[0] == nil && commitErrs[1] == nil {
		t.Fatalf("stranded molecule: both last-step closes committed and %s is %s with every step closed; one unit of work must conflict",
			root, readStatusInUOW(t, ctx, kit, root))
	}
	for i, err := range commitErrs {
		if err != nil && !isSerializationError(err) {
			t.Fatalf("commit %d error = %v, want a serialization conflict", i, err)
		}
	}
}

// TestMoleculeAutoCloseSiblingClosesConflictBeforeTheLastStepInUOW is the
// unit-of-work twin of the dolt store's pin on the coordination cell's cost:
// two concurrent sibling closes under an open auto-closing root conflict even
// while a third step keeps the molecule from completing. If this stops
// conflicting, the docs that state the cost are stale.
func TestMoleculeAutoCloseSiblingClosesConflictBeforeTheLastStepInUOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "molsib")
	kit := newUOWRoleFixtureKit(provider, "molsib")

	commitErrs := closeTwoStepsInRacingUOWs(t, ctx, provider, seedRacingMoleculeInUOW(t, ctx, kit, kit.IssuePrefix+"-siblings", 3))
	t.Logf("sibling commits: %v", commitErrs)
	if commitErrs[0] == nil && commitErrs[1] == nil {
		t.Fatal("two concurrent sibling closes under an open auto-closing root both committed; want one serialization conflict")
	}
	for i, err := range commitErrs {
		if err != nil && !isSerializationError(err) {
			t.Fatalf("commit %d error = %v, want a serialization conflict", i, err)
		}
	}
}

// raceMoleculeClosesInUOW seeds an auto-closing molecule of n steps under
// root and closes every step at once through lifecycle, whose RunTxResult
// replays each unit of work that loses the coordination conflict. Every close
// must land, and the root must end closed, reported by exactly one of them.
func raceMoleculeClosesInUOW(t *testing.T, ctx context.Context, kit roleFixtureKit, lifecycle publicops.Lifecycle, root string, n int) {
	t.Helper()
	steps := seedRacingMoleculeInUOW(t, ctx, kit, root, n)
	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		results = make([]publicops.CloseResult, n)
		errs    = make([]error, n)
	)
	for i, step := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = lifecycle.Close(ctx, publicops.CloseRequest{
				Actor: "tester", IssueID: step, Reason: "done", AutoCloseMolecule: true,
			})
		}()
	}
	close(start)
	wg.Wait()

	reported := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("close %s: %v", steps[i], err)
		}
		if results[i].AutoClosedMolecule != nil {
			reported++
		}
	}
	if status := readStatusInUOW(t, ctx, kit, root); status != string(types.StatusClosed) {
		t.Fatalf("%s is %s after all %d of its steps closed, want closed", root, status, n)
	}
	if reported != 1 {
		t.Errorf("%s was reported by %d closes, want exactly one", root, reported)
	}
}

// TestMoleculeAutoCloseConcurrentLastStepsCloseTheRootInUOW races the closes
// of a molecule's last two steps through this backend's Lifecycle role, whose
// RunTxResult replays the unit of work that loses the coordination conflict:
// in every round the root ends closed, reported by exactly one of the closes.
func TestMoleculeAutoCloseConcurrentLastStepsCloseTheRootInUOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "molagg")
	kit := newUOWRoleFixtureKit(provider, "molagg")
	lifecycle, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}

	const rounds = 8
	for round := range rounds {
		raceMoleculeClosesInUOW(t, ctx, kit, lifecycle, fmt.Sprintf("%s-race-%d", kit.IssuePrefix, round), 2)
	}
}

// TestMoleculeAutoCloseSiblingFanOutClosesTheRootInUOW is the unit-of-work
// twin of the dolt store's fan-out: all the steps of one molecule closed at
// once serialize on the root's coordination cell, and RunTxResult carries
// every one of them to a commit, closing the root exactly once.
func TestMoleculeAutoCloseSiblingFanOutClosesTheRootInUOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "molfan")
	kit := newUOWRoleFixtureKit(provider, "molfan")
	lifecycle, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}

	raceMoleculeClosesInUOW(t, ctx, kit, lifecycle, kit.IssuePrefix+"-fanout", 6)
}
