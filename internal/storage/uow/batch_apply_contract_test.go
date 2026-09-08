package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchApplyContract runs the BatchApplier contract against the
// unit-of-work provider, and this is the leg that makes the contract a TWO-VOTE
// one. It is a genuinely independent body: the shared store body composes
// issueops.ExecuteCreate, ExecuteUpdate and ExecuteClose, all of which take a
// *sql.Tx, and a unit of work's runner is a *sql.Conn — so this leg re-derives
// every rule the role promises through the domain use cases, reaching only the
// SHARED pieces it can (storage.PlanApplyBatch for the request rules,
// issueops.ApplyBatchCommitMessage for the entry).
//
// It is therefore the leg where a disagreement about what a batch MEANS shows
// up, not just a wrapper losing a field: the as-modified guards, the end gate
// and the plane routing are written twice.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global and
// the history deltas are only meaningful while the subtests run sequentially.
func TestBatchApplyContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWBatchApplyFixture(t, ctx, "bapply")

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

func newUOWBatchApplyFixture(t *testing.T, ctx context.Context, prefix string) conformance.BatchApplyFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewBatchApplier: a provider that
	// stopped offering the role is the regression a constructor call would hide.
	source, ok := provider.(BatchApplierSource)
	if !ok {
		t.Fatalf("provider %T does not offer the BatchApplier accessor", provider)
	}
	applier, err := source.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.BatchApplyFixture{
		IssuePrefix:          kit.IssuePrefix,
		BatchApplier:         applier,
		CreateIssue:          kit.CreateIssue,
		CreateWisp:           kit.CreateWisp,
		QueryScalar:          kit.QueryScalar,
		CountHistory:         kit.CountHistory,
		CountHistoryMatching: kit.CountHistoryMatching,
		// OUT OF BAND: the frozen kit publishes no commit hook, and this backend
		// has no ambient connection to commit on, so settling is one empty unit
		// of work carrying a message. RunTx demotes it to a plain SQL commit when
		// nothing is pending, so it records no entry of its own on a clean
		// working set — which is what a settle hook has to promise.
		CommitPending: func(ctx context.Context) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "bd: settle the batch-apply contract's seeds", nil
			})
		},
	}
}
