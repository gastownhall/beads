package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchCreatorContract runs the BatchCreator contract against the
// server-backed store, which reaches the shared body
// (issueops.ExecuteCreateBatch: prepare every item, assign every id, then one
// CreateIssuesInTxWithResult). It is ONE of two votes on that body; the
// embedded wiring is the same body on a different engine.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch: the history cases take a before/after delta,
// which is only meaningful while the subtests run sequentially. setupTestStore
// already marks the PARENT parallel; no subtest here calls t.Parallel.
func TestBatchCreatorContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchCreatorFixture(t, "bcr")
	defer cleanup()

	t.Run("CreatesEveryItemAsOneAct", func(t *testing.T) {
		conformance.RunBatchCreatorCreatesEveryItemAsOneAct(t, ctx, fixture)
	})
	t.Run("RefusesEverythingWhenOneItemRefuses", func(t *testing.T) {
		conformance.RunBatchCreatorRefusesEverythingWhenOneItemRefuses(t, ctx, fixture)
	})
	t.Run("RejectsAnUnusableRequest", func(t *testing.T) {
		conformance.RunBatchCreatorRejectsAnUnusableRequest(t, ctx, fixture)
	})
	t.Run("RefusesACrossPlaneInBatchEdge", func(t *testing.T) {
		conformance.RunBatchCreatorRefusesACrossPlaneInBatchEdge(t, ctx, fixture)
	})
	t.Run("LinksAnEarlierItemOfTheSameBatch", func(t *testing.T) {
		conformance.RunBatchCreatorLinksAnEarlierItemOfTheSameBatch(t, ctx, fixture)
	})
	t.Run("LinksAnEarlierItemOnTheEphemeralPlane", func(t *testing.T) {
		conformance.RunBatchCreatorLinksAnEarlierItemOnTheEphemeralPlane(t, ctx, fixture)
	})
	t.Run("KeepsAnEphemeralItemsLabelsOffTheDurablePlane", func(t *testing.T) {
		conformance.RunBatchCreatorKeepsAnEphemeralItemsLabelsOffTheDurablePlane(t, ctx, fixture)
	})
	t.Run("RefusesAnAbsentEdgeTarget", func(t *testing.T) {
		conformance.RunBatchCreatorRefusesAnAbsentEdgeTarget(t, ctx, fixture)
	})
	t.Run("AcceptsAForeignEdgeTarget", func(t *testing.T) {
		conformance.RunBatchCreatorAcceptsAForeignEdgeTarget(t, ctx, fixture)
	})
	t.Run("RecordsOneHistoryEntry", func(t *testing.T) {
		conformance.RunBatchCreatorRecordsOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("RecordsNoHistoryForAnEphemeralBatch", func(t *testing.T) {
		conformance.RunBatchCreatorRecordsNoHistoryForAnEphemeralBatch(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunBatchCreatorDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// newDoltBatchCreatorFixture composes the frozen role kit with this backend's
// accessor.
func newDoltBatchCreatorFixture(t *testing.T, prefix string) (conformance.BatchCreatorFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	creator, err := store.BatchCreator()
	if err != nil {
		stop()
		t.Fatalf("BatchCreator(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.BatchCreatorFixture{
		IssuePrefix:  kit.IssuePrefix,
		BatchCreator: creator,
		CreateIssue:  kit.CreateIssue,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}, ctx, stop
}
