package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCrossRecordInvariantContract wires this leg into the R16.1
// cross-record invariant contract. Phase 0 leaves every hook nil in every
// backend's fixture kit (architecture §12), so each case below skips by
// name; this file exists so TestEveryLegWiresEveryRoleContract counts this
// leg, and so the cases start running for real the moment this leg's fixture
// kit grows a non-nil hook.
func TestCrossRecordInvariantContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.CrossRecordInvariantFixture{IssuePrefix: "xrec"}

	t.Run("SurvivesTwoPassingPerRecordGuards", func(t *testing.T) {
		conformance.RunCrossRecordInvariantSurvivesTwoPassingPerRecordGuards(t, ctx, fixture)
	})
	t.Run("StoreInvariantTransactionScopesExactlyTheSpanningRecords", func(t *testing.T) {
		conformance.RunStoreInvariantTransactionScopesExactlyTheSpanningRecords(t, ctx, fixture)
	})
	t.Run("InvariantRefusalNamesTheViolatedInvariant", func(t *testing.T) {
		conformance.RunInvariantRefusalNamesTheViolatedInvariant(t, ctx, fixture)
	})
	t.Run("MemoryKeyAliasUniquenessIsAStoreInvariantNotACAS", func(t *testing.T) {
		conformance.RunMemoryKeyAliasUniquenessIsAStoreInvariantNotACAS(t, ctx, fixture)
	})
	t.Run("MaximumEndpointMultiplicityIsAStoreInvariantNotACAS", func(t *testing.T) {
		conformance.RunMaximumEndpointMultiplicityIsAStoreInvariantNotACAS(t, ctx, fixture)
	})
	t.Run("AdvisoryLeaseAloneDoesNotPreventTheInvariantViolation", func(t *testing.T) {
		conformance.RunAdvisoryLeaseAloneDoesNotPreventTheInvariantViolation(t, ctx, fixture)
	})
	t.Run("LeaseAcquisitionRecordsNoHistoryEntry", func(t *testing.T) {
		conformance.RunLeaseAcquisitionRecordsNoHistoryEntry(t, ctx, fixture)
	})
}
