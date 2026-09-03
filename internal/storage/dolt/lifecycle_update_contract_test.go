package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLifecycleUpdateContract runs the accessor-only Update half of the
// Lifecycle contract against the server-backed store. It shares its
// validate/execute body with the embedded store
// (internal/storage/issueops.ExecuteUpdate), so this wiring and the embedded one
// are ONE vote on the semantics; the unit-of-work wiring is the second.
//
// The cases are subtests of one parent so the whole block shares one store and
// one copy-on-write branch: each Run namespaces its ids under the fixture
// prefix. setupTestStore already marks the PARENT parallel; no subtest here
// calls t.Parallel.
func TestLifecycleUpdateContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltLifecycleUpdateFixture(t, "lup")
	defer cleanup()

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

func newDoltLifecycleUpdateFixture(t *testing.T, prefix string) (conformance.LifecycleUpdateFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the capability accessor, not NewIssueOperations: a store that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.LifecycleUpdateFixture{
		IssuePrefix:          kit.IssuePrefix,
		Lifecycle:            lifecycle,
		CreateIssue:          kit.CreateIssue,
		CreateWisp:           kit.CreateWisp,
		AddDependency:        kit.AddDependency,
		SetConfig:            kit.SetConfig,
		CountHistoryMatching: kit.CountHistoryMatching,
		// The frozen kit reads through QueryScalar. This block reads its
		// post-state through the store's own reads instead, so no case in it
		// depends on raw SQL.
		GetIssue:         store.GetIssue,
		ListEvents:       newDoltContractEventLister(store),
		ListDependencies: newDoltContractDependencyLister(store),
		WispExists:       newDoltContractWispProbe(store),
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
