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

func TestEmbeddedIssueOperationsCreateUnderAParentMintsTheNextChildID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "childmint")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateUnderAParentMintsTheNextChildID(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "childmint"))
}

func TestEmbeddedIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "metaevent")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "metaevent"))
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

func TestEmbeddedIssueOperationsCreateClosedDerivesTheClosedStamp(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "createclosed")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateClosedDerivesTheClosedStamp(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "createclosed"))
}

func TestEmbeddedIssueOperationsUpdateWritesEveryScalarPatchField(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "scalarsurface")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateWritesEveryScalarPatchField(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "scalarsurface"))
}

func TestEmbeddedIssueOperationsUpdateStampsStartedAtOnceOnTheFirstInProgress(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "startstamp")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateStampsStartedAtOnceOnTheFirstInProgress(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "startstamp"))
}

func TestEmbeddedIssueOperationsUpdateRawMetadataTakesTheFunnelsValueShapes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rawmeta")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateRawMetadataTakesTheFunnelsValueShapes(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "rawmeta"))
}

func TestEmbeddedIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "typevocab")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "typevocab"))
}

func TestEmbeddedIssueOperationsUpdateStatusCrossingSettlesDependers(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bsupd")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateStatusCrossingSettlesDependers(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "bsupd"))
}

func TestEmbeddedIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bscond")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "bscond"))
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
