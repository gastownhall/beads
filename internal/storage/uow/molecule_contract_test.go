package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
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
