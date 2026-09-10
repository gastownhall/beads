package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// TestLifecycleUpdateContract runs the accessor-only Update half of the
// Lifecycle contract against the unit-of-work provider — the one Lifecycle
// implementation that does not share the validate/execute body the two stores
// share. It maps the patch itself and derives Changed by comparing the
// post-state snapshot to the pre-state one instead of reading the row-write
// facts, so this is the wiring where a same-value or clear divergence shows up.
//
// One provider for the whole block (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so a parallel subtest would share another subtest's
// database.
func TestLifecycleUpdateContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWLifecycleUpdateFixture(t, ctx, "lup")

	t.Run("PersistsThePatchAndHydratesTheResult", func(t *testing.T) {
		conformance.RunLifecycleUpdatePersistsThePatchAndHydratesTheResult(t, ctx, fixture)
	})
	t.Run("PreservesTheCreationStamp", func(t *testing.T) {
		conformance.RunLifecycleUpdatePreservesTheCreationStamp(t, ctx, fixture)
	})
	t.Run("ReportsNoChangeForASameValuePatch", func(t *testing.T) {
		conformance.RunLifecycleUpdateReportsNoChangeForASameValuePatch(t, ctx, fixture)
	})
	t.Run("AppendsNotesWithoutReplacingThem", func(t *testing.T) {
		conformance.RunLifecycleUpdateAppendsNotesWithoutReplacingThem(t, ctx, fixture)
	})
	t.Run("ClearsTheNullableMembers", func(t *testing.T) {
		conformance.RunLifecycleUpdateClearsTheNullableMembers(t, ctx, fixture)
	})
	t.Run("ReplacesTheLabelSet", func(t *testing.T) {
		conformance.RunLifecycleUpdateReplacesTheLabelSet(t, ctx, fixture)
	})
	t.Run("ResolvesBothPlanesUnlessRestricted", func(t *testing.T) {
		conformance.RunLifecycleUpdateResolvesBothPlanesUnlessRestricted(t, ctx, fixture)
	})
	t.Run("RefusesUnknownIDsAndActorlessRequests", func(t *testing.T) {
		conformance.RunLifecycleUpdateRefusesUnknownIDsAndActorlessRequests(t, ctx, fixture)
	})
	t.Run("RefusalWritesNoMemberOfThePatch", func(t *testing.T) {
		conformance.RunLifecycleUpdateRefusalWritesNoMemberOfThePatch(t, ctx, fixture)
	})
	t.Run("ConditionalGuardsGateOrdinaryEdits", func(t *testing.T) {
		conformance.RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits(t, ctx, fixture)
	})
	t.Run("ConditionalGuardAcceptsRespelledAssignee", func(t *testing.T) {
		conformance.RunLifecycleUpdateConditionalGuardAcceptsRespelledAssignee(t, ctx, fixture)
	})
	t.Run("MetadataPatchOrdersMergeSetUnset", func(t *testing.T) {
		conformance.RunLifecycleUpdateMetadataPatchOrdersMergeSetUnset(t, ctx, fixture)
	})
	t.Run("ClosePolicy", func(t *testing.T) {
		conformance.RunLifecycleUpdateClosePolicy(t, ctx, fixture)
	})
	t.Run("AssigneeTransferFence", func(t *testing.T) {
		conformance.RunLifecycleUpdateAssigneeTransferFence(t, ctx, fixture)
	})
	t.Run("ClaimIsAMutationWhenThePatchRestoresTheRow", func(t *testing.T) {
		conformance.RunLifecycleUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t, ctx, fixture)
	})
	t.Run("ParentIDReplacesTheParentEdge", func(t *testing.T) {
		conformance.RunLifecycleUpdateParentIDReplacesTheParentEdge(t, ctx, fixture)
	})
	t.Run("ParentIDReplacesEveryParent", func(t *testing.T) {
		conformance.RunLifecycleUpdateParentIDReplacesEveryParent(t, ctx, fixture)
	})
	t.Run("PersistentPreservesUnversionedClass", func(t *testing.T) {
		conformance.RunLifecycleUpdatePersistentPreservesUnversionedClass(t, ctx, fixture)
	})
	t.Run("ProvenanceLabelsHistory", func(t *testing.T) {
		conformance.RunLifecycleUpdateProvenanceLabelsHistory(t, ctx, fixture)
	})
}

func newUOWLifecycleUpdateFixture(t *testing.T, ctx context.Context, prefix string) conformance.LifecycleUpdateFixture {
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
	return conformance.LifecycleUpdateFixture{
		IssuePrefix:          kit.IssuePrefix,
		Lifecycle:            lifecycle,
		CreateIssue:          kit.CreateIssue,
		CreateWisp:           kit.CreateWisp,
		AddDependency:        kit.AddDependency,
		SetConfig:            kit.SetConfig,
		CountHistoryMatching: kit.CountHistoryMatching,
		ListEvents:           newUOWContractEventLister(provider),
		ListDependencies:     newUOWContractDependencyLister(provider),
		WispExists:           newUOWContractWispProbe(provider),
		// The frozen kit reads through QueryScalar. This block reads its
		// post-state through the backend's own both-plane issue read instead,
		// so no case in it depends on raw SQL.
		//
		// The label set is read straight off the label use case rather than
		// through hydrateIssueOperation, which is the code the result-shape
		// assertions are ABOUT: a readback sharing it could not tell a result
		// that lost its labels from a row that never had them.
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
