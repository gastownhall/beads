//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMetadataCASContract runs the MetadataCAS contract against the embedded
// store, which hands back the SAME body the server-backed store does
// (internal/storage/issueops.CompareAndSetMetadataKeyInTx) and differs only in
// how it reaches a transaction and in that its version commit is published
// after that transaction rather than inside it. That is what this wiring
// catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, every case namespaces its ids, and the history
// deltas need the subtests sequential anyway.
func TestMetadataCASContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "mcas")
	ctx := t.Context()
	fixture := newEmbeddedMetadataCASFixture(t, te, "mcas")

	t.Run("CreatesAKeyThatWasAbsent", func(t *testing.T) {
		conformance.RunMetadataCASCreatesAKeyThatWasAbsent(t, ctx, fixture)
	})
	t.Run("RefusesASecondCreateAndReportsTheHolder", func(t *testing.T) {
		conformance.RunMetadataCASRefusesASecondCreateAndReportsTheHolder(t, ctx, fixture)
	})
	t.Run("SwapsOnAMatchAndReportsTheNewValue", func(t *testing.T) {
		conformance.RunMetadataCASSwapsOnAMatchAndReportsTheNewValue(t, ctx, fixture)
	})
	t.Run("RefusalReportsTheCurrentValueAndWritesNothing", func(t *testing.T) {
		conformance.RunMetadataCASRefusalReportsTheCurrentValueAndWritesNothing(t, ctx, fixture)
	})
	t.Run("ComparesCanonically", func(t *testing.T) {
		conformance.RunMetadataCASComparesCanonically(t, ctx, fixture)
	})
	t.Run("ReportsTheValueTheRowHolds", func(t *testing.T) {
		conformance.RunMetadataCASReportsTheValueTheRowHolds(t, ctx, fixture)
	})
	t.Run("DistinguishesAnAbsentKeyFromAStoredNull", func(t *testing.T) {
		conformance.RunMetadataCASDistinguishesAnAbsentKeyFromAStoredNull(t, ctx, fixture)
	})
	t.Run("RemovesTheKeyWhenTheValueIsAbsent", func(t *testing.T) {
		conformance.RunMetadataCASRemovesTheKeyWhenTheValueIsAbsent(t, ctx, fixture)
	})
	t.Run("PreservesSiblingKeys", func(t *testing.T) {
		conformance.RunMetadataCASPreservesSiblingKeys(t, ctx, fixture)
	})
	t.Run("NoOpSwapWritesNothing", func(t *testing.T) {
		conformance.RunMetadataCASNoOpSwapWritesNothing(t, ctx, fixture)
	})
	t.Run("RefusesAnIDOnNeitherPlane", func(t *testing.T) {
		conformance.RunMetadataCASRefusesAnIDOnNeitherPlane(t, ctx, fixture)
	})
	t.Run("RefusesAnUnusableRequest", func(t *testing.T) {
		conformance.RunMetadataCASRefusesAnUnusableRequest(t, ctx, fixture)
	})
	t.Run("ResolvesAWispAnchor", func(t *testing.T) {
		conformance.RunMetadataCASResolvesAWispAnchor(t, ctx, fixture)
	})
	t.Run("AWispSwapRecordsNoDurableHistory", func(t *testing.T) {
		conformance.RunMetadataCASAWispSwapRecordsNoDurableHistory(t, ctx, fixture)
	})
	t.Run("RecordsExactlyOneHistoryEntry", func(t *testing.T) {
		conformance.RunMetadataCASRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("HistoryEntryNamesTheActor", func(t *testing.T) {
		conformance.RunMetadataCASHistoryEntryNamesTheActor(t, ctx, fixture)
	})
	t.Run("ARefusedSwapRecordsNoHistory", func(t *testing.T) {
		conformance.RunMetadataCASARefusedSwapRecordsNoHistory(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunMetadataCASDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

func newEmbeddedMetadataCASFixture(t *testing.T, te *testEnv, prefix string) conformance.MetadataCASFixture {
	t.Helper()
	cas, err := te.store.MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.MetadataCASFixture{
		IssuePrefix:   kit.IssuePrefix,
		MetadataCAS:   cas,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: embeddedCommitPending(te),
	}
}
