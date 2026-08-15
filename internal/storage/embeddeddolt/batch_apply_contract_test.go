//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchApplyContract runs the BatchApplier contract against the embedded
// store, which hands back the SAME body the server-backed store does
// (internal/storage/issueops.ApplyBatchInTx) and differs only in how it reaches
// a transaction and in that its version commit is published after that
// transaction rather than inside it. That is what this wiring catches; it is
// not an independent vote on the body — the unit-of-work leg is the second
// vote. See the contract file's header.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, every case namespaces its ids, and the history
// deltas need the subtests sequential anyway.
func TestBatchApplyContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bapply")
	ctx := t.Context()
	fixture := newEmbeddedBatchApplyFixture(t, te, "bapply")

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

// newEmbeddedBatchApplyFixture composes the frozen role kit with this backend's
// accessor. Nothing adapts between the two.
func newEmbeddedBatchApplyFixture(t *testing.T, te *testEnv, prefix string) conformance.BatchApplyFixture {
	t.Helper()
	applier, err := te.store.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
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
			_, err := te.store.CommitPending(ctx, "batch-apply-contract")
			return err
		},
	}
}
