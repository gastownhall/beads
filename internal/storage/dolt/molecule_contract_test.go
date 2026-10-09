package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMoleculeContract runs the molecule contract (in-transaction auto-close on
// Lifecycle.Close and BatchCloser.CloseBatch, and the MoleculeStepper) against
// the server-backed store. One store, sequential subtests, ids namespaced by
// the fixture prefix.
func TestMoleculeContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltMoleculeFixture(t, "mol")
	defer cleanup()

	for _, test := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.MoleculeFixture)
	}{
		{"CloseAutoClosesTheCompletedRoot", conformance.RunMoleculeCloseAutoClosesTheCompletedRoot},
		{"AutoCloseIsOptInAndRuleBound", conformance.RunMoleculeAutoCloseIsOptInAndRuleBound},
		{"RecloseHealsAStrandedRoot", conformance.RunMoleculeRecloseHealsAStrandedRoot},
		{"BatchCloseAutoClosesTheRootOnce", conformance.RunMoleculeBatchCloseAutoClosesTheRootOnce},
		{"RefusedRootKeepsTheStepClose", conformance.RunMoleculeRefusedRootKeepsTheStepClose},
		{"StepperClaimsTheNextReadyStepForTheActor", conformance.RunMoleculeStepperClaimsTheNextReadyStepForTheActor},
		{"StepperNeverTakesAStepAnotherActorHolds", conformance.RunMoleculeStepperNeverTakesAStepAnotherActorHolds},
		{"StepperAnswersCompleteAndOutsideAMolecule", conformance.RunMoleculeStepperAnswersCompleteAndOutsideAMolecule},
		{"StepperClaimsAWispStep", conformance.RunMoleculeStepperClaimsAWispStep},
	} {
		t.Run(test.name, func(t *testing.T) { test.run(t, ctx, fixture) })
	}
}

func newDoltMoleculeFixture(t *testing.T, prefix string) (conformance.MoleculeFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	cleanup := func() {
		cancel()
		storeCleanup()
	}
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		cleanup()
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	closer, err := store.BatchCloser()
	if err != nil {
		cleanup()
		t.Fatalf("BatchCloser(): %v", err)
	}
	stepper, err := store.MoleculeStepper()
	if err != nil {
		cleanup()
		t.Fatalf("MoleculeStepper(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.MoleculeFixture{
		IssuePrefix:   kit.IssuePrefix,
		Lifecycle:     lifecycle,
		BatchCloser:   closer,
		Stepper:       stepper,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
	}, ctx, cleanup
}
