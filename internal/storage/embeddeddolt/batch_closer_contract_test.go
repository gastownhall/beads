//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The embedded store's wiring of the BatchCloser contract. It shares the
// validate/execute body with the server-backed store, so what this leg adds is
// the embedded transaction wrapper and the embedded engine, not a second
// independent reading of the contract.

func TestEmbeddedBatchCloserOutcomesMirrorItemsIndexForIndex(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserOutcomesMirrorItemsIndexForIndex(t, ctx, newEmbeddedBatchCloserFixture(t, "bcmirror"))
}

func TestEmbeddedBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit(t, ctx, newEmbeddedBatchCloserFixture(t, "bcrefusal"))
}

func TestEmbeddedBatchCloserOutcomeSnapshotIsTheDocumentedShape(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserOutcomeSnapshotIsTheDocumentedShape(t, ctx, newEmbeddedBatchCloserFixture(t, "bcshape"))
}

func TestEmbeddedBatchCloserRequestValidationReturnsZeroResultAndChangesNothing(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserRequestValidationReturnsZeroResultAndChangesNothing(t, ctx, newEmbeddedBatchCloserFixture(t, "bcvalid"))
}

func TestEmbeddedBatchCloserClaimFilterValueFailureIsARequestValidationFailure(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure(t, ctx, newEmbeddedBatchCloserFixture(t, "bcbadsort"))
}

func TestEmbeddedBatchCloserBackendFailureReturnsNoOutcomes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserBackendFailureReturnsNoOutcomes(t, ctx, newEmbeddedBatchCloserFixture(t, "bcdeadctx"))
}

func TestEmbeddedBatchCloserIdempotentRecloseIsAPerItemSuccess(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserIdempotentRecloseIsAPerItemSuccess(t, ctx, newEmbeddedBatchCloserFixture(t, "bcreclose"))
}

func TestEmbeddedBatchCloserAllIdempotentBatchLandsNothing(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserAllIdempotentBatchLandsNothing(t, ctx, newEmbeddedBatchCloserFixture(t, "bcallidem"))
}

func TestEmbeddedBatchCloserDuplicateItemRecloseAtItsOwnIndex(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserDuplicateItemRecloseAtItsOwnIndex(t, ctx, newEmbeddedBatchCloserFixture(t, "bcdup"))
}

func TestEmbeddedBatchCloserWispItemClosesAndEarnsTheClaim(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserWispItemClosesAndEarnsTheClaim(t, ctx, newEmbeddedBatchCloserFixture(t, "bcwispclaim"))
}

func TestEmbeddedBatchCloserDurableHistoryNeverNamesAWisp(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserDurableHistoryNeverNamesAWisp(t, ctx, newEmbeddedBatchCloserFixture(t, "bcwisphistory"))
}

func TestEmbeddedBatchCloserForceBypassesOnlyClosePolicy(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserForceBypassesOnlyClosePolicy(t, ctx, newEmbeddedBatchCloserFixture(t, "bcforce"))
}

func TestEmbeddedBatchCloserClaimNextHydratesWhenSomethingClosed(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserClaimNextHydratesWhenSomethingClosed(t, ctx, newEmbeddedBatchCloserFixture(t, "bcclaimhit"))
}

func TestEmbeddedBatchCloserClaimNextIsNilWhenNothingClosed(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserClaimNextIsNilWhenNothingClosed(t, ctx, newEmbeddedBatchCloserFixture(t, "bcclaimnone"))
}

func TestEmbeddedBatchCloserClaimNextIsNilWhenTheFrontIsEmpty(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserClaimNextIsNilWhenTheFrontIsEmpty(t, ctx, newEmbeddedBatchCloserFixture(t, "bcclaimempty"))
}

func TestEmbeddedBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch(t, ctx, newEmbeddedBatchCloserFixture(t, "bcclaimunblock"))
}

func TestEmbeddedBatchCloserRecordsOneHistoryEntryForWhatLanded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserRecordsOneHistoryEntryForWhatLanded(t, ctx, newEmbeddedBatchCloserFixture(t, "bchistory"))
}

func TestEmbeddedBatchCloserAllRefusedBatchRecordsNoHistory(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserAllRefusedBatchRecordsNoHistory(t, ctx, newEmbeddedBatchCloserFixture(t, "bcnohistory"))
}

func TestEmbeddedBatchCloserDoesNotMutateTheCallerRequest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunBatchCloserDoesNotMutateTheCallerRequest(t, ctx, newEmbeddedBatchCloserFixture(t, "bcsnapshot"))
}

func newEmbeddedBatchCloserFixture(t *testing.T, prefix string) conformance.BatchCloserFixture {
	t.Helper()
	te := newTestEnv(t, prefix)
	closer, err := te.store.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.BatchCloserFixture{
		IssuePrefix:   kit.IssuePrefix,
		Closer:        closer,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// Local, because the shared kit exposes no comment hook. Folding it in
		// is an S0 follow-up (bd-kue5t); editing the frozen kit from this slice
		// would collide with the five running beside it.
		AddComment:   te.store.AddComment,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
