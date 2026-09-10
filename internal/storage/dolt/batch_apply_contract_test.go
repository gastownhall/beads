package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchApplyContract runs the BatchApplier contract against the
// server-backed store, which reaches the shared body
// (internal/storage/issueops.ApplyBatchInTx) through its own retrying write
// transaction and composes the commit message inside it, because the default
// message names how much LANDED.
//
// It is ONE of TWO votes: the embedded wiring is the same body on a different
// engine, and only the unit-of-work leg is an independent implementation. See
// the contract file's header.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. setupTestStore already marks the PARENT
// parallel; no subtest here calls t.Parallel, and the history cases take
// before/after deltas that are only meaningful while they run sequentially.
func TestBatchApplyContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchApplyFixture(t, "bapply")
	defer cleanup()

	t.Run("AppliesEveryItemInDeclarationOrder", func(t *testing.T) {
		conformance.RunBatchApplyAppliesEveryItemInDeclarationOrder(t, ctx, fixture)
	})
	t.Run("BindsEachNamedKeyToItsMintedID", func(t *testing.T) {
		conformance.RunBatchApplyBindsEachNamedKeyToItsMintedID(t, ctx, fixture)
	})
	t.Run("ResolvesABackwardKeyRef", func(t *testing.T) {
		conformance.RunBatchApplyResolvesABackwardKeyRef(t, ctx, fixture)
	})
	t.Run("RefusesAKeyDeclaredLater", func(t *testing.T) {
		conformance.RunBatchApplyRefusesAKeyDeclaredLater(t, ctx, fixture)
	})
	t.Run("RefusesAKeyNoItemDeclares", func(t *testing.T) {
		conformance.RunBatchApplyRefusesAKeyNoItemDeclares(t, ctx, fixture)
	})
	t.Run("RefusesARefNamingNeitherOrBoth", func(t *testing.T) {
		conformance.RunBatchApplyRefusesARefNamingNeitherOrBoth(t, ctx, fixture)
	})
	t.Run("RollsBackEverythingWhenTheLastItemRefuses", func(t *testing.T) {
		conformance.RunBatchApplyRollsBackEverythingWhenTheLastItemRefuses(t, ctx, fixture)
	})
	t.Run("NeverReordersItsItems", func(t *testing.T) {
		conformance.RunBatchApplyNeverReordersItsItems(t, ctx, fixture)
	})
	t.Run("EndGateRefusesAHierarchyTheRequestBuilt", func(t *testing.T) {
		conformance.RunBatchApplyEndGateRefusesAHierarchyTheRequestBuilt(t, ctx, fixture)
	})
	t.Run("EndGateCycleSurvivesSkipPerEdgeCycleCheck", func(t *testing.T) {
		conformance.RunBatchApplyEndGateCycleSurvivesSkipPerEdgeCycleCheck(t, ctx, fixture)
	})
	t.Run("ExpectedVersionThatMatchesLetsTheItemThrough", func(t *testing.T) {
		conformance.RunBatchApplyExpectedVersionThatMatchesLetsTheItemThrough(t, ctx, fixture)
	})
	t.Run("StaleExpectedVersionRefusesTheWholeRequest", func(t *testing.T) {
		conformance.RunBatchApplyStaleExpectedVersionRefusesTheWholeRequest(t, ctx, fixture)
	})
	t.Run("RefusesExpectedVersionOnARowAnEarlierItemTouched", func(t *testing.T) {
		conformance.RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemTouched(t, ctx, fixture)
	})
	t.Run("RefusesExpectedVersionOnARowAnEarlierItemCreated", func(t *testing.T) {
		conformance.RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemCreated(t, ctx, fixture)
	})
	t.Run("EvaluatesExpectedStatusAsModified", func(t *testing.T) {
		conformance.RunBatchApplyEvaluatesExpectedStatusAsModified(t, ctx, fixture)
	})
	t.Run("EvaluatesExpectedAssigneeAsModified", func(t *testing.T) {
		conformance.RunBatchApplyEvaluatesExpectedAssigneeAsModified(t, ctx, fixture)
	})
	t.Run("ClosePolicyEvaluatesAtTheCloseItem", func(t *testing.T) {
		conformance.RunBatchApplyClosePolicyEvaluatesAtTheCloseItem(t, ctx, fixture)
	})
	t.Run("AllowsAClosedParentToGainAnOpenChild", func(t *testing.T) {
		conformance.RunBatchApplyAllowsAClosedParentToGainAnOpenChild(t, ctx, fixture)
	})
	t.Run("UpdateAfterCloseInOneRequest", func(t *testing.T) {
		conformance.RunBatchApplyUpdateAfterCloseInOneRequest(t, ctx, fixture)
	})
	t.Run("ReportsChangedPerItem", func(t *testing.T) {
		conformance.RunBatchApplyReportsChangedPerItem(t, ctx, fixture)
	})
	t.Run("ANoOpBatchRecordsNoHistory", func(t *testing.T) {
		conformance.RunBatchApplyANoOpBatchRecordsNoHistory(t, ctx, fixture)
	})
	t.Run("RecordsOneEntryForAWriteThatLandedNothing", func(t *testing.T) {
		conformance.RunBatchApplyRecordsOneEntryForAWriteThatLandedNothing(t, ctx, fixture)
	})
	t.Run("RecordsExactlyOneHistoryEntry", func(t *testing.T) {
		conformance.RunBatchApplyRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("HistoryNamesTheActorAndReadsTheProvenance", func(t *testing.T) {
		conformance.RunBatchApplyHistoryNamesTheActorAndReadsTheProvenance(t, ctx, fixture)
	})
	t.Run("ARefusedRequestRecordsNoHistory", func(t *testing.T) {
		conformance.RunBatchApplyARefusedRequestRecordsNoHistory(t, ctx, fixture)
	})
	t.Run("AnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory", func(t *testing.T) {
		conformance.RunBatchApplyAnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory(t, ctx, fixture)
	})
	t.Run("RefusesACrossPlaneEdgeBetweenRowsItCreated", func(t *testing.T) {
		conformance.RunBatchApplyRefusesACrossPlaneEdgeBetweenRowsItCreated(t, ctx, fixture)
	})
	t.Run("AcceptsAnExternalEdgeTarget", func(t *testing.T) {
		conformance.RunBatchApplyAcceptsAnExternalEdgeTarget(t, ctx, fixture)
	})
	t.Run("NormalizesTheWaitsForGate", func(t *testing.T) {
		conformance.RunBatchApplyNormalizesTheWaitsForGate(t, ctx, fixture)
	})
	t.Run("SplicesAForwardMetadataRef", func(t *testing.T) {
		conformance.RunBatchApplySplicesAForwardMetadataRef(t, ctx, fixture)
	})
	t.Run("SplicesASelfMetadataRef", func(t *testing.T) {
		conformance.RunBatchApplySplicesASelfMetadataRef(t, ctx, fixture)
	})
	t.Run("RefusesAMetadataRefNoItemDeclares", func(t *testing.T) {
		conformance.RunBatchApplyRefusesAMetadataRefNoItemDeclares(t, ctx, fixture)
	})
	t.Run("TheSpliceRecordsAnUpdateEvent", func(t *testing.T) {
		conformance.RunBatchApplyTheSpliceRecordsAnUpdateEvent(t, ctx, fixture)
	})
	t.Run("KeepsAStoredNullApartFromAnEmptyString", func(t *testing.T) {
		conformance.RunBatchApplyKeepsAStoredNullApartFromAnEmptyString(t, ctx, fixture)
	})
	t.Run("LandsAnIdempotencyRecordWithItsWork", func(t *testing.T) {
		conformance.RunBatchApplyLandsAnIdempotencyRecordWithItsWork(t, ctx, fixture)
	})
	t.Run("BoundsTheItemCount", func(t *testing.T) {
		conformance.RunBatchApplyBoundsTheItemCount(t, ctx, fixture)
	})
	t.Run("ReplayMintsANewSetOfRows", func(t *testing.T) {
		conformance.RunBatchApplyReplayMintsANewSetOfRows(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunBatchApplyDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnUnusableRequest", func(t *testing.T) {
		conformance.RunBatchApplyRefusesAnUnusableRequest(t, ctx, fixture)
	})
}

// newDoltBatchApplyFixture composes the frozen role kit with this backend's
// accessor. Nothing adapts between the two: the kit's hooks are assignable to
// the fixture fields of the same name.
func newDoltBatchApplyFixture(t *testing.T, prefix string) (conformance.BatchApplyFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	applier, err := store.BatchApplier()
	if err != nil {
		stop()
		t.Fatalf("BatchApplier(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.BatchApplyFixture{
		IssuePrefix:          kit.IssuePrefix,
		BatchApplier:         applier,
		CreateIssue:          kit.CreateIssue,
		CreateWisp:           kit.CreateWisp,
		QueryScalar:          kit.QueryScalar,
		CountHistory:         kit.CountHistory,
		CountHistoryMatching: kit.CountHistoryMatching,
		// OUT OF BAND: the frozen kit reaches the issues and config planes only
		// and publishes no commit hook, so the history cases get theirs from the
		// store's own batch-commit seam. It is a no-op when the working set is
		// clean, which is what "settle whatever is pending" has to mean.
		CommitPending: func(ctx context.Context) error {
			_, err := store.CommitPending(ctx, "batch-apply-contract")
			return err
		},
	}, ctx, stop
}
