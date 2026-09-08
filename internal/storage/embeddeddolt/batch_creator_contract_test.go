//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchCreatorContract runs the BatchCreator contract against the embedded
// store, which reaches the SAME body the server-backed store does
// (issueops.ExecuteCreateBatch) and differs only in the engine underneath. That
// is what this wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids are prefix-namespaced, and the history
// cases need the subtests sequential anyway.
func TestBatchCreatorContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bcr")
	ctx := t.Context()
	fixture := newEmbeddedBatchCreatorFixture(t, te, "bcr")

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

func newEmbeddedBatchCreatorFixture(t *testing.T, te *testEnv, prefix string) conformance.BatchCreatorFixture {
	t.Helper()
	creator, err := te.store.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.BatchCreatorFixture{
		IssuePrefix:  kit.IssuePrefix,
		BatchCreator: creator,
		CreateIssue:  kit.CreateIssue,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
