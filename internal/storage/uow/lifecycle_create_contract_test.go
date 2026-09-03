package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// TestLifecycleCreateContract runs the accessor-only Create half of the
// Lifecycle contract against the unit-of-work provider — the one Lifecycle
// implementation that does not share the create body the two stores share. It
// copies the request into its own createParams, so this is the wiring where a
// field dropped on the way in shows up.
//
// One provider for the whole block (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so a parallel subtest would share another subtest's
// database.
func TestLifecycleCreateContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWLifecycleCreateFixture(t, ctx, "lcc")

	t.Run("RejectsMissingDependencyTargets", func(t *testing.T) {
		conformance.RunLifecycleCreateRejectsMissingDependencyTargets(t, ctx, fixture)
	})
	t.Run("RefusesAnOccupiedID", func(t *testing.T) {
		conformance.RunLifecycleCreateRefusesAnOccupiedID(t, ctx, fixture)
	})
	t.Run("RefusesAForeignIDPrefix", func(t *testing.T) {
		conformance.RunLifecycleCreateRefusesAForeignIDPrefix(t, ctx, fixture)
	})
	t.Run("InheritsParentLabels", func(t *testing.T) {
		conformance.RunLifecycleCreateInheritsParentLabels(t, ctx, fixture)
	})
	t.Run("WritesEveryScalarField", func(t *testing.T) {
		conformance.RunLifecycleCreateWritesEveryScalarField(t, ctx, fixture)
	})
}

func newUOWLifecycleCreateFixture(t *testing.T, ctx context.Context, prefix string) conformance.LifecycleCreateFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewIssueOperations: a provider that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	source, ok := provider.(IssueLifecycleSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueLifecycle accessor", provider)
	}
	lifecycle, err := source.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.LifecycleCreateFixture{
		IssuePrefix: kit.IssuePrefix,
		Lifecycle:   lifecycle,
		CreateIssue: kit.CreateIssue,
		WispExists:  newUOWContractWispProbe(provider),
		// The frozen kit reads through QueryScalar. This block reads its
		// post-state through the backend's own both-plane issue read instead,
		// so no case in it depends on raw SQL.
		//
		// The label set is read straight off the label use case rather than
		// through the create's own hydration, which is the code the
		// result-shape assertions are ABOUT: a readback sharing it could not
		// tell a result that lost its labels from a row that never had them.
		GetIssue: func(ctx context.Context, id string) (*types.Issue, error) {
			return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
				issue, isWisp, err := operationIssue(ctx, uw, id, false)
				if err != nil {
					return nil, err
				}
				if isWisp {
					issue.Labels, err = uw.LabelUseCase().GetWispLabels(ctx, id)
				} else {
					issue.Labels, err = uw.LabelUseCase().GetLabels(ctx, id)
				}
				if err != nil {
					return nil, err
				}
				return issue, nil
			})
		},
	}
}
