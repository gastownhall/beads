//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMoleculeContract runs the molecule contract against the embedded store.
func TestMoleculeContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "mol")
	ctx := t.Context()
	fixture := newEmbeddedMoleculeFixture(t, te, "mol")

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

func newEmbeddedMoleculeFixture(t *testing.T, te *testEnv, prefix string) conformance.MoleculeFixture {
	t.Helper()
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	closer, err := te.store.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser(): %v", err)
	}
	stepper, err := te.store.MoleculeStepper()
	if err != nil {
		t.Fatalf("MoleculeStepper(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
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
