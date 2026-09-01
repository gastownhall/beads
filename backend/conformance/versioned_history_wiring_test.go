package conformance

import (
	"context"
	"testing"
)

// TestVersionedHistoryPhase0RunsInFullSkipMode is Phase 0's own wiring: it
// constructs each of the three new Fixtures with every hook nil — exactly
// the state every backend's real fixture kit is in as of this phase's merge
// (architecture §12: "every new hook is nil in every backend's fixture kit")
// — and runs every R16/R16.1/R17/R20 case once each, proving the whole suite
// compiles and skips cleanly under today's CI (NFR-01).
//
// Each leg under internal/storage wires these same entrypoints, with the same
// all-nil fixtures, so TestEveryLegWiresEveryRoleContract counts them; this
// local copy is not a substitute for those, it just proves the all-skip
// behavior once without needing to build all three legs to see it.
func TestVersionedHistoryPhase0RunsInFullSkipMode(t *testing.T) {
	ctx := context.Background()

	t.Run("ExpectedRevision", func(t *testing.T) {
		fixture := ExpectedRevisionFixture{IssuePrefix: "vh0"}
		t.Run("AcceptsAWriteNamingTheCurrentVersion", func(t *testing.T) {
			RunExpectedRevisionAcceptsAWriteNamingTheCurrentVersion(t, ctx, fixture)
		})
		t.Run("AcceptsAWriteNamingNoVersion", func(t *testing.T) {
			RunExpectedRevisionAcceptsAWriteNamingNoVersion(t, ctx, fixture)
		})
		t.Run("CoversFieldsOutsideAnyWatchedSubset", func(t *testing.T) {
			RunExpectedRevisionCoversFieldsOutsideAnyWatchedSubset(t, ctx, fixture)
		})
		t.Run("RefusalReportsTheRefusingVersionAddress", func(t *testing.T) {
			RunRefusalReportsTheRefusingVersionAddress(t, ctx, fixture)
		})
		t.Run("RefusalReportsTheRefusingVersionsChangeAttribution", func(t *testing.T) {
			RunRefusalReportsTheRefusingVersionsChangeAttribution(t, ctx, fixture)
		})
		t.Run("RefusalIsATypedOutcomeNotAGenericError", func(t *testing.T) {
			RunRefusalIsATypedOutcomeNotAGenericError(t, ctx, fixture)
		})
		t.Run("RefusalIsDistinguishableFromAnAcceptedWrite", func(t *testing.T) {
			RunRefusalIsDistinguishableFromAnAcceptedWrite(t, ctx, fixture)
		})
		t.Run("RefusalIsDistinguishableFromNotFound", func(t *testing.T) {
			RunRefusalIsDistinguishableFromNotFound(t, ctx, fixture)
		})
		t.Run("RefusalIsDistinguishableFromValidationFailure", func(t *testing.T) {
			RunRefusalIsDistinguishableFromValidationFailure(t, ctx, fixture)
		})
		t.Run("RefusalNeverSilentlyPicksAWinner", func(t *testing.T) {
			RunRefusalNeverSilentlyPicksAWinner(t, ctx, fixture)
		})
	})

	t.Run("CrossRecordInvariant", func(t *testing.T) {
		fixture := CrossRecordInvariantFixture{IssuePrefix: "vh0"}
		t.Run("SurvivesTwoPassingPerRecordGuards", func(t *testing.T) {
			RunCrossRecordInvariantSurvivesTwoPassingPerRecordGuards(t, ctx, fixture)
		})
		t.Run("StoreInvariantTransactionScopesExactlyTheSpanningRecords", func(t *testing.T) {
			RunStoreInvariantTransactionScopesExactlyTheSpanningRecords(t, ctx, fixture)
		})
		t.Run("InvariantRefusalNamesTheViolatedInvariant", func(t *testing.T) {
			RunInvariantRefusalNamesTheViolatedInvariant(t, ctx, fixture)
		})
		t.Run("MemoryKeyAliasUniquenessIsAStoreInvariantNotACAS", func(t *testing.T) {
			RunMemoryKeyAliasUniquenessIsAStoreInvariantNotACAS(t, ctx, fixture)
		})
		t.Run("MaximumEndpointMultiplicityIsAStoreInvariantNotACAS", func(t *testing.T) {
			RunMaximumEndpointMultiplicityIsAStoreInvariantNotACAS(t, ctx, fixture)
		})
		t.Run("AdvisoryLeaseAloneDoesNotPreventTheInvariantViolation", func(t *testing.T) {
			RunAdvisoryLeaseAloneDoesNotPreventTheInvariantViolation(t, ctx, fixture)
		})
		t.Run("LeaseAcquisitionRecordsNoHistoryEntry", func(t *testing.T) {
			RunLeaseAcquisitionRecordsNoHistoryEntry(t, ctx, fixture)
		})
	})

	t.Run("Retention", func(t *testing.T) {
		fixture := RetentionFixture{IssuePrefix: "vh0"}
		t.Run("RemovalLeavesTheAddressAbleToAnswer", func(t *testing.T) {
			RunRemovalLeavesTheAddressAbleToAnswer(t, ctx, fixture)
		})
		t.Run("RemovalReasonIsRetentionErasureOrReorganizationDistinctly", func(t *testing.T) {
			RunRemovalReasonIsRetentionErasureOrReorganizationDistinctly(t, ctx, fixture)
		})
		t.Run("RemovalReportsTheSurvivingRetainedWindow", func(t *testing.T) {
			RunRemovalReportsTheSurvivingRetainedWindow(t, ctx, fixture)
		})
		t.Run("RemovalNeverReassignsASurvivingAddress", func(t *testing.T) {
			RunRemovalNeverReassignsASurvivingAddress(t, ctx, fixture)
		})
		t.Run("GoneIsDistinguishableFromUnknown", func(t *testing.T) {
			RunGoneIsDistinguishableFromUnknown(t, ctx, fixture)
		})
		t.Run("AnAddressNeverResolvesToADifferentState", func(t *testing.T) {
			RunAnAddressNeverResolvesToADifferentState(t, ctx, fixture)
		})
		t.Run("ADestructiveOperationEnumeratesAffectedAddressesFirst", func(t *testing.T) {
			RunADestructiveOperationEnumeratesAffectedAddressesFirst(t, ctx, fixture)
		})
		t.Run("AHoldPreventsRemovalAndReportsInRetainedBounds", func(t *testing.T) {
			RunAHoldPreventsRemovalAndReportsInRetainedBounds(t, ctx, fixture)
		})
		t.Run("ForcingAHeldRemovalRecordsWhoWhenWhy", func(t *testing.T) {
			RunForcingAHeldRemovalRecordsWhoWhenWhy(t, ctx, fixture)
		})
		t.Run("EveryRetentionAnswerNamesItsProducingStore", func(t *testing.T) {
			RunEveryRetentionAnswerNamesItsProducingStore(t, ctx, fixture)
		})
		t.Run("AStoreWithNoLineageKnowledgeAnswersUnknownNotGone", func(t *testing.T) {
			RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone(t, ctx, fixture)
		})
		t.Run("AStoreThatRemovesStateStillAnswersGoneDurably", func(t *testing.T) {
			RunAStoreThatRemovesStateStillAnswersGoneDurably(t, ctx, fixture)
		})
		t.Run("ErasureMintsACorrectedVersionRatherThanEditingInPlace", func(t *testing.T) {
			RunErasureMintsACorrectedVersionRatherThanEditingInPlace(t, ctx, fixture)
		})
	})

	t.Run("Epoch", func(t *testing.T) {
		fixture := EpochFixture{IssuePrefix: "vh0"}
		t.Run("AnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange", func(t *testing.T) {
			RunAnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange(t, ctx, fixture)
		})
		t.Run("EpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed", func(t *testing.T) {
			RunEpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed(t, ctx, fixture)
		})
	})
}
