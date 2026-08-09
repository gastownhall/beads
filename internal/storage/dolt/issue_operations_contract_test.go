package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateRoutesInfraTypesToWisps(t, ctx, fixture)
}

func TestIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateRejectsMissingDependencyTargets(t, ctx, fixture)
}

func TestIssueOperationsCreateRefusesAnOccupiedID(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateRefusesAnOccupiedID(t, ctx, fixture)
}

func TestIssueOperationsCreateInheritsParentLabels(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateInheritsParentLabels(t, ctx, fixture)
}

func TestIssueOperationsCreateRefusesAForeignIDPrefix(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateRefusesAForeignIDPrefix(t, ctx, fixture)
}

func TestIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t, ctx, fixture)
}

func TestIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset(t, ctx, fixture)
}

func TestIssueOperationsUpdateClosePolicy(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateClosePolicy(t, ctx, fixture)
}

func TestIssueOperationsUpdateAssigneeTransferFence(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateAssigneeTransferFence(t, ctx, fixture)
}

func TestIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits(t, ctx, fixture)
}

func TestIssueOperationsUpdateClosedFieldsMatchClose(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateClosedFieldsMatchClose(t, ctx, fixture)
}

func TestIssueOperationsUpdateClaimConflictCarriesTheLosingState(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateClaimConflictCarriesTheLosingState(t, ctx, fixture)
}

func TestIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t, ctx, fixture)
}

func TestIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t, ctx, fixture)
}

func TestIssueOperationsUpdateLabelPatchOrdering(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateLabelPatchOrdering(t, ctx, fixture)
}

func TestIssueOperationsUpdateLabelPatchValueRules(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateLabelPatchValueRules(t, ctx, fixture)
}

func TestIssueOperationsUpdateParentIDReplacesTheParentEdge(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateParentIDReplacesTheParentEdge(t, ctx, fixture)
}

func TestIssueOperationsUpdateParentIDReplacesEveryParent(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateParentIDReplacesEveryParent(t, ctx, fixture)
}

func TestIssueOperationsUpdateMetadataReplaceClearsAndValidates(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateMetadataReplaceClearsAndValidates(t, ctx, fixture)
}

func TestIssueOperationsRequestValuesAreNotMutated(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsRequestValuesAreNotMutated(t, ctx, fixture)
}

func TestIssueOperationsUpdateProvenanceLabelsHistory(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateProvenanceLabelsHistory(t, ctx, fixture)
}

func TestIssueOperationsUpdatePersistentPreservesUnversionedClass(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdatePersistentPreservesUnversionedClass(t, ctx, fixture)
}

func TestIssueOperationsCreateWritesEveryScalarField(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateWritesEveryScalarField(t, ctx, fixture)
}

func TestIssueOperationsUpdateWritesEveryScalarPatchField(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateWritesEveryScalarPatchField(t, ctx, fixture)
}

func TestIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t, ctx, fixture)
}

func TestIssueOperationsUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t, ctx, fixture)
}

func TestIssueOperationsUpdateStatusCrossingSettlesDependers(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateStatusCrossingSettlesDependers(t, ctx, fixture)
}

func TestIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender(t, ctx, fixture)
}

func TestIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction(t, ctx, fixture)
}

func TestIssueOperationsClaimLeavesBlockedStateAlone(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsClaimLeavesBlockedStateAlone(t, ctx, fixture)
}

func newDoltIssueOperationsFixture(t *testing.T) (conformance.IssueOperationsStagingFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	operations, err := NewIssueOperations(store)
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("NewIssueOperations: %v", err)
	}
	kit := newDoltRoleFixtureKit(store, "test")
	fixture := conformance.IssueOperationsStagingFixture{
		IssuePrefix: "test",
		Operations:  operations,
		CreateIssue: store.CreateIssue,
		SetConfig:   store.SetConfig,
		UpdateRaw:   store.UpdateIssue,
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			return store.db.QueryRowContext(ctx, query, args...).Scan(dest...)
		},
		CountHistoryMatching: kit.CountHistoryMatching,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
