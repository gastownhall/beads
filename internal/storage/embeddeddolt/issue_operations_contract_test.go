//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func TestEmbeddedIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "infra")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateRoutesInfraTypesToWisps(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "infra"))
}

func TestEmbeddedIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "skipdep")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateRejectsMissingDependencyTargets(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "skipdep"))
}

func TestEmbeddedIssueOperationsCreateRefusesAnOccupiedID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "occupied")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateRefusesAnOccupiedID(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "occupied"))
}

func TestEmbeddedIssueOperationsCreateRefusesAForeignIDPrefix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "foreignpfx")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateRefusesAForeignIDPrefix(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "foreignpfx"))
}

func TestEmbeddedIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "metaorder")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "metaorder"))
}

func TestEmbeddedIssueOperationsCreateInheritsParentLabels(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "inherit")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateInheritsParentLabels(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "inherit"))
}

func TestEmbeddedIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "metaevent")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "metaevent"))
}

func TestEmbeddedIssueOperationsUpdateClosePolicy(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "closepol")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateClosePolicy(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "closepol"))
}

func TestEmbeddedIssueOperationsUpdateAssigneeTransferFence(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "xferfence")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateAssigneeTransferFence(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "xferfence"))
}

func TestEmbeddedIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "guardgate")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "guardgate"))
}

func TestEmbeddedIssueOperationsUpdateClosedFieldsMatchClose(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "closedfields")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateClosedFieldsMatchClose(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "closedfields"))
}

func TestEmbeddedIssueOperationsUpdateClaimConflictCarriesTheLosingState(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "claimconflict")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateClaimConflictCarriesTheLosingState(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "claimconflict"))
}

func TestEmbeddedIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "customclaim")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "customclaim"))
}

func TestEmbeddedIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "planeonly")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "planeonly"))
}

func TestEmbeddedIssueOperationsUpdateLabelPatchOrdering(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "labelpatch")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateLabelPatchOrdering(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "labelpatch"))
}

func TestEmbeddedIssueOperationsUpdateLabelPatchValueRules(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "labelvalues")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateLabelPatchValueRules(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "labelvalues"))
}

func TestEmbeddedIssueOperationsUpdateParentIDReplacesTheParentEdge(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "reparent")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateParentIDReplacesTheParentEdge(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "reparent"))
}

func TestEmbeddedIssueOperationsUpdateParentIDReplacesEveryParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "multiparent")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateParentIDReplacesEveryParent(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "multiparent"))
}

func TestEmbeddedIssueOperationsUpdateMetadataReplaceClearsAndValidates(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "metareplace")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateMetadataReplaceClearsAndValidates(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "metareplace"))
}

func TestEmbeddedIssueOperationsRequestValuesAreNotMutated(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "detach")
	ctx := t.Context()
	conformance.RunIssueOperationsRequestValuesAreNotMutated(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "detach"))
}

func TestEmbeddedIssueOperationsUpdateProvenanceLabelsHistory(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "provenance")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateProvenanceLabelsHistory(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "provenance"))
}

func TestEmbeddedIssueOperationsUpdatePersistentPreservesUnversionedClass(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "unversioned")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdatePersistentPreservesUnversionedClass(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "unversioned"))
}

func TestEmbeddedIssueOperationsCreateWritesEveryScalarField(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "createsurface")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateWritesEveryScalarField(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "createsurface"))
}

func TestEmbeddedIssueOperationsUpdateWritesEveryScalarPatchField(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "scalarsurface")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateWritesEveryScalarPatchField(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "scalarsurface"))
}

func TestEmbeddedIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "typevocab")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "typevocab"))
}

func TestEmbeddedIssueOperationsUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "claimrestore")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "claimrestore"))
}

func TestEmbeddedIssueOperationsUpdateStatusCrossingSettlesDependers(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bsupd")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateStatusCrossingSettlesDependers(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "bsupd"))
}

func TestEmbeddedIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bscreate")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "bscreate"))
}

func TestEmbeddedIssueOperationsClaimLeavesBlockedStateAlone(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bsclaim")
	ctx := t.Context()
	conformance.RunIssueOperationsClaimLeavesBlockedStateAlone(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "bsclaim"))
}

func newEmbeddedIssueOperationsFixture(t *testing.T, ctx context.Context, te *testEnv, prefix string) conformance.IssueOperationsStagingFixture {
	t.Helper()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	return conformance.IssueOperationsStagingFixture{
		IssuePrefix: prefix,
		Operations:  operations,
		CreateIssue: te.store.CreateIssue,
		SetConfig:   te.store.SetConfig,
		UpdateRaw:   te.store.UpdateIssue,
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			te.queryScalar(t, ctx, query, args, dest...)
			return nil
		},
	}
}
