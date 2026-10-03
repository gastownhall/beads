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
//
// Each subtest below calls testContext(t) for its OWN context rather than
// sharing one built once in newDoltBatchApplyFixture. Only the deadline is
// per-subtest; the store and branch above stay shared, and ordering stays
// sequential. See newDoltBatchApplyFixture's doc comment for why.
func TestBatchApplyContract(t *testing.T) {
	fixture, cleanup := newDoltBatchApplyFixture(t, "bapply")
	defer cleanup()

	t.Run("AppliesEveryItemInDeclarationOrder", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyAppliesEveryItemInDeclarationOrder(t, ctx, fixture)
	})
	t.Run("BindsEachNamedKeyToItsMintedID", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyBindsEachNamedKeyToItsMintedID(t, ctx, fixture)
	})
	t.Run("ResolvesABackwardKeyRef", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyResolvesABackwardKeyRef(t, ctx, fixture)
	})
	t.Run("RefusesAKeyDeclaredLater", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesAKeyDeclaredLater(t, ctx, fixture)
	})
	t.Run("RefusesAKeyNoItemDeclares", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesAKeyNoItemDeclares(t, ctx, fixture)
	})
	t.Run("RefusesARefNamingNeitherOrBoth", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesARefNamingNeitherOrBoth(t, ctx, fixture)
	})
	t.Run("RollsBackEverythingWhenTheLastItemRefuses", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRollsBackEverythingWhenTheLastItemRefuses(t, ctx, fixture)
	})
	t.Run("NeverReordersItsItems", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyNeverReordersItsItems(t, ctx, fixture)
	})
	t.Run("EndGateRefusesAHierarchyTheRequestBuilt", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyEndGateRefusesAHierarchyTheRequestBuilt(t, ctx, fixture)
	})
	t.Run("EndGateCycleSurvivesSkipPerEdgeCycleCheck", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyEndGateCycleSurvivesSkipPerEdgeCycleCheck(t, ctx, fixture)
	})
	t.Run("ExpectedVersionThatMatchesLetsTheItemThrough", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyExpectedVersionThatMatchesLetsTheItemThrough(t, ctx, fixture)
	})
	t.Run("StaleExpectedVersionRefusesTheWholeRequest", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyStaleExpectedVersionRefusesTheWholeRequest(t, ctx, fixture)
	})
	t.Run("RefusesExpectedVersionOnARowAnEarlierItemTouched", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemTouched(t, ctx, fixture)
	})
	t.Run("RefusesExpectedVersionOnARowAnEarlierItemCreated", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemCreated(t, ctx, fixture)
	})
	t.Run("EvaluatesExpectedStatusAsModified", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyEvaluatesExpectedStatusAsModified(t, ctx, fixture)
	})
	t.Run("EvaluatesExpectedAssigneeAsModified", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyEvaluatesExpectedAssigneeAsModified(t, ctx, fixture)
	})
	t.Run("ClosePolicyEvaluatesAtTheCloseItem", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyClosePolicyEvaluatesAtTheCloseItem(t, ctx, fixture)
	})
	t.Run("AllowsAClosedParentToGainAnOpenChild", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyAllowsAClosedParentToGainAnOpenChild(t, ctx, fixture)
	})
	t.Run("UpdateAfterCloseInOneRequest", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyUpdateAfterCloseInOneRequest(t, ctx, fixture)
	})
	t.Run("ReportsChangedPerItem", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyReportsChangedPerItem(t, ctx, fixture)
	})
	t.Run("ANoOpBatchRecordsNoHistory", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyANoOpBatchRecordsNoHistory(t, ctx, fixture)
	})
	t.Run("RecordsOneEntryForAWriteThatLandedNothing", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRecordsOneEntryForAWriteThatLandedNothing(t, ctx, fixture)
	})
	t.Run("RecordsExactlyOneHistoryEntry", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("HistoryNamesTheActorAndReadsTheProvenance", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyHistoryNamesTheActorAndReadsTheProvenance(t, ctx, fixture)
	})
	t.Run("ARefusedRequestRecordsNoHistory", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyARefusedRequestRecordsNoHistory(t, ctx, fixture)
	})
	t.Run("AnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyAnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory(t, ctx, fixture)
	})
	t.Run("RefusesACrossPlaneEdgeBetweenRowsItCreated", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesACrossPlaneEdgeBetweenRowsItCreated(t, ctx, fixture)
	})
	t.Run("AcceptsAnExternalEdgeTarget", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyAcceptsAnExternalEdgeTarget(t, ctx, fixture)
	})
	t.Run("NormalizesTheWaitsForGate", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyNormalizesTheWaitsForGate(t, ctx, fixture)
	})
	t.Run("SplicesAForwardMetadataRef", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplySplicesAForwardMetadataRef(t, ctx, fixture)
	})
	t.Run("SplicesASelfMetadataRef", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplySplicesASelfMetadataRef(t, ctx, fixture)
	})
	t.Run("RefusesAMetadataRefNoItemDeclares", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesAMetadataRefNoItemDeclares(t, ctx, fixture)
	})
	t.Run("TheSpliceRecordsAnUpdateEvent", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyTheSpliceRecordsAnUpdateEvent(t, ctx, fixture)
	})
	t.Run("KeepsAStoredNullApartFromAnEmptyString", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyKeepsAStoredNullApartFromAnEmptyString(t, ctx, fixture)
	})
	t.Run("LandsAnIdempotencyRecordWithItsWork", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyLandsAnIdempotencyRecordWithItsWork(t, ctx, fixture)
	})
	t.Run("BoundsTheItemCount", func(t *testing.T) {
		// 3x the package's usual testTimeout, not the standard budget: this
		// subtest applies MaxApplyBatchItems (1000) real items and then
		// MaxApplyBatchItems+1 more, so it does ~1000x the DB work of a
		// typical subtest here. Measured at 66s against the standard 90s
		// testTimeout on the server tier (25% headroom) - the same class of
		// flake risk testTimeout's own doc comment describes for host
		// contention. A dedicated 3x budget gives this one subtest realistic
		// headroom without raising the shared testTimeout (and therefore
		// every `5*testTimeout` cleanup bound) for every other subtest that
		// doesn't need it.
		ctx, cancel := context.WithTimeout(context.Background(), 3*testTimeout)
		defer cancel()
		conformance.RunBatchApplyBoundsTheItemCount(t, ctx, fixture)
	})
	t.Run("ReplayMintsANewSetOfRows", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyReplayMintsANewSetOfRows(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnUnusableRequest", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		conformance.RunBatchApplyRefusesAnUnusableRequest(t, ctx, fixture)
	})
}

// newDoltBatchApplyFixture composes the frozen role kit with this backend's
// accessor. Nothing adapts between the two: the kit's hooks are assignable to
// the fixture fields of the same name.
//
// It deliberately does NOT hand back a shared context: every subtest below
// calls testContext(t) for its own freshly-timed context instead of sharing
// one built here. A single context created once, for every subtest in this
// parent, meant one slow subtest (BoundsTheItemCount's real 1000+1001-item
// apply against the server-Dolt tier) could blow the shared deadline and
// cascade "context deadline exceeded" into every subtest that happened to run
// after it — hiding the real failure behind a wall of unrelated red subtests.
// This mirrors openStoreWithOwnBudget's rationale above (be-gvnsq): giving
// each unit of work its own budget removes the coupling. The STORE and its
// copy-on-write branch are still shared across every subtest, and subtests
// still run sequentially (no t.Parallel) — only the context deadline is
// decoupled.
func newDoltBatchApplyFixture(t *testing.T, prefix string) (conformance.BatchApplyFixture, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	applier, err := store.BatchApplier()
	if err != nil {
		storeCleanup()
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
	}, storeCleanup
}
