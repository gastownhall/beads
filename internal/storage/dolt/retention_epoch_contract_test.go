package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestRetentionContract wires this leg into the R20 retention contract.
// Phase 0 leaves every hook nil in every backend's fixture kit (architecture
// §12), so each case below skips by name; this file exists so
// TestEveryLegWiresEveryRoleContract counts this leg, and so the cases start
// running for real the moment this leg's fixture kit grows a non-nil hook.
func TestRetentionContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.RetentionFixture{IssuePrefix: "rete"}

	t.Run("RemovalLeavesTheAddressAbleToAnswer", func(t *testing.T) {
		conformance.RunRemovalLeavesTheAddressAbleToAnswer(t, ctx, fixture)
	})
	t.Run("RemovalReasonIsRetentionErasureOrReorganizationDistinctly", func(t *testing.T) {
		conformance.RunRemovalReasonIsRetentionErasureOrReorganizationDistinctly(t, ctx, fixture)
	})
	t.Run("RemovalReportsTheSurvivingRetainedWindow", func(t *testing.T) {
		conformance.RunRemovalReportsTheSurvivingRetainedWindow(t, ctx, fixture)
	})
	t.Run("RemovalNeverReassignsASurvivingAddress", func(t *testing.T) {
		conformance.RunRemovalNeverReassignsASurvivingAddress(t, ctx, fixture)
	})
	t.Run("GoneIsDistinguishableFromUnknown", func(t *testing.T) {
		conformance.RunGoneIsDistinguishableFromUnknown(t, ctx, fixture)
	})
	t.Run("AnAddressNeverResolvesToADifferentState", func(t *testing.T) {
		conformance.RunAnAddressNeverResolvesToADifferentState(t, ctx, fixture)
	})
	t.Run("ADestructiveOperationEnumeratesAffectedAddressesFirst", func(t *testing.T) {
		conformance.RunADestructiveOperationEnumeratesAffectedAddressesFirst(t, ctx, fixture)
	})
	t.Run("AHoldPreventsRemovalAndReportsInRetainedBounds", func(t *testing.T) {
		conformance.RunAHoldPreventsRemovalAndReportsInRetainedBounds(t, ctx, fixture)
	})
	t.Run("ForcingAHeldRemovalRecordsWhoWhenWhy", func(t *testing.T) {
		conformance.RunForcingAHeldRemovalRecordsWhoWhenWhy(t, ctx, fixture)
	})
	t.Run("EveryRetentionAnswerNamesItsProducingStore", func(t *testing.T) {
		conformance.RunEveryRetentionAnswerNamesItsProducingStore(t, ctx, fixture)
	})
	t.Run("AStoreWithNoLineageKnowledgeAnswersUnknownNotGone", func(t *testing.T) {
		conformance.RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone(t, ctx, fixture)
	})
	t.Run("AStoreThatRemovesStateStillAnswersGoneDurably", func(t *testing.T) {
		conformance.RunAStoreThatRemovesStateStillAnswersGoneDurably(t, ctx, fixture)
	})
	t.Run("ErasureMintsACorrectedVersionRatherThanEditingInPlace", func(t *testing.T) {
		conformance.RunErasureMintsACorrectedVersionRatherThanEditingInPlace(t, ctx, fixture)
	})
}

// TestEpochContract wires this leg into the R20 epoch contract. Same Phase 0
// all-nil state as TestRetentionContract above.
func TestEpochContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.EpochFixture{IssuePrefix: "epch"}

	t.Run("AnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange", func(t *testing.T) {
		conformance.RunAnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange(t, ctx, fixture)
	})
	t.Run("EpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed", func(t *testing.T) {
		conformance.RunEpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed(t, ctx, fixture)
	})
}
