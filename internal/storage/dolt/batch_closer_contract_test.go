package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The server-backed store's wiring of the BatchCloser contract. Each case gets
// its own store, which on this backend is its own copy-on-write branch, so the
// history deltas the atomicity cases take cannot be moved by a sibling.

func TestBatchCloserOutcomesMirrorItemsIndexForIndex(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcmirror")
	defer cleanup()
	conformance.RunBatchCloserOutcomesMirrorItemsIndexForIndex(t, ctx, fixture)
}

func TestBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcrefusal")
	defer cleanup()
	conformance.RunBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit(t, ctx, fixture)
}

func TestBatchCloserOutcomeSnapshotIsTheDocumentedShape(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcshape")
	defer cleanup()
	conformance.RunBatchCloserOutcomeSnapshotIsTheDocumentedShape(t, ctx, fixture)
}

func TestBatchCloserRequestValidationReturnsZeroResultAndChangesNothing(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcvalid")
	defer cleanup()
	conformance.RunBatchCloserRequestValidationReturnsZeroResultAndChangesNothing(t, ctx, fixture)
}

func TestBatchCloserClaimFilterValueFailureIsARequestValidationFailure(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcbadsort")
	defer cleanup()
	conformance.RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure(t, ctx, fixture)
}

func TestBatchCloserBackendFailureReturnsNoOutcomes(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcdeadctx")
	defer cleanup()
	conformance.RunBatchCloserBackendFailureReturnsNoOutcomes(t, ctx, fixture)
}

func TestBatchCloserIdempotentRecloseIsAPerItemSuccess(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcreclose")
	defer cleanup()
	conformance.RunBatchCloserIdempotentRecloseIsAPerItemSuccess(t, ctx, fixture)
}

func TestBatchCloserAllIdempotentBatchLandsNothing(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcallidem")
	defer cleanup()
	conformance.RunBatchCloserAllIdempotentBatchLandsNothing(t, ctx, fixture)
}

func TestBatchCloserDuplicateItemRecloseAtItsOwnIndex(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcdup")
	defer cleanup()
	conformance.RunBatchCloserDuplicateItemRecloseAtItsOwnIndex(t, ctx, fixture)
}

func TestBatchCloserWispItemClosesAndEarnsTheClaim(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcwispclaim")
	defer cleanup()
	conformance.RunBatchCloserWispItemClosesAndEarnsTheClaim(t, ctx, fixture)
}

func TestBatchCloserDurableHistoryNeverNamesAWisp(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcwisphistory")
	defer cleanup()
	conformance.RunBatchCloserDurableHistoryNeverNamesAWisp(t, ctx, fixture)
}

func TestBatchCloserForceBypassesOnlyClosePolicy(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcforce")
	defer cleanup()
	conformance.RunBatchCloserForceBypassesOnlyClosePolicy(t, ctx, fixture)
}

func TestBatchCloserClaimNextHydratesWhenSomethingClosed(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcclaimhit")
	defer cleanup()
	conformance.RunBatchCloserClaimNextHydratesWhenSomethingClosed(t, ctx, fixture)
}

func TestBatchCloserClaimNextIsNilWhenNothingClosed(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcclaimnone")
	defer cleanup()
	conformance.RunBatchCloserClaimNextIsNilWhenNothingClosed(t, ctx, fixture)
}

func TestBatchCloserClaimNextIsNilWhenTheFrontIsEmpty(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcclaimempty")
	defer cleanup()
	conformance.RunBatchCloserClaimNextIsNilWhenTheFrontIsEmpty(t, ctx, fixture)
}

func TestBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcclaimunblock")
	defer cleanup()
	conformance.RunBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch(t, ctx, fixture)
}

func TestBatchCloserRecordsOneHistoryEntryForWhatLanded(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bchistory")
	defer cleanup()
	conformance.RunBatchCloserRecordsOneHistoryEntryForWhatLanded(t, ctx, fixture)
}

func TestBatchCloserAllRefusedBatchRecordsNoHistory(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcnohistory")
	defer cleanup()
	conformance.RunBatchCloserAllRefusedBatchRecordsNoHistory(t, ctx, fixture)
}

func TestBatchCloserDoesNotMutateTheCallerRequest(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcsnapshot")
	defer cleanup()
	conformance.RunBatchCloserDoesNotMutateTheCallerRequest(t, ctx, fixture)
}

func TestBatchCloserSettlesTheDependersOfWhatItClosed(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCloserFixture(t, "bcblocked")
	defer cleanup()
	conformance.RunBatchCloserSettlesTheDependersOfWhatItClosed(t, ctx, fixture)
}

func newDoltBatchCloserFixture(t *testing.T, prefix string) (conformance.BatchCloserFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	closer, err := store.BatchCloser()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("BatchCloser(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.BatchCloserFixture{
		IssuePrefix:   kit.IssuePrefix,
		Closer:        closer,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// Local, because the shared kit exposes no comment hook. Folding it in
		// is an S0 follow-up (bd-kue5t); editing the frozen kit from this slice
		// would collide with the five running beside it.
		AddComment:   store.AddComment,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
