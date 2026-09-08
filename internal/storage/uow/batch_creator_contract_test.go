package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchCreatorContract runs the BatchCreator contract against the
// unit-of-work provider — the one implementation that does NOT reach
// issueops.ExecuteCreateBatch. It creates item by item through the domain use
// case and writes each item's edges as it writes that item, so it keeps "all or
// nothing" by returning early out of a loop and letting the transaction roll
// back rather than by never having written. It is the SECOND of two votes, not
// the third: the two store backends share the other body.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global.
func TestBatchCreatorContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWBatchCreatorFixture(t, ctx, "bcr")

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

func newUOWBatchCreatorFixture(t *testing.T, ctx context.Context, prefix string) conformance.BatchCreatorFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewBatchCreator: a provider that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	source, ok := provider.(BatchCreatorSource)
	if !ok {
		t.Fatalf("provider %T does not offer the BatchCreator accessor", provider)
	}
	creator, err := source.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.BatchCreatorFixture{
		IssuePrefix:  kit.IssuePrefix,
		BatchCreator: creator,
		CreateIssue:  kit.CreateIssue,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
