package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMetadataCASContract runs the MetadataCAS contract against the
// server-backed store, which reaches
// internal/storage/issueops.CompareAndSetMetadataKeyInTx through its own
// retrying write transaction and is the one leg whose version-control entry is
// recorded INSIDE that transaction; the other two publish theirs after it.
//
// All three legs run that one body, so this is not an independent vote on the
// comparison — it is the check on THIS leg's wrapper. See the contract file's
// header.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. setupTestStore already marks the PARENT
// parallel; no subtest here calls t.Parallel, and the two history cases take
// before/after deltas that are only meaningful while they run sequentially.
func TestMetadataCASContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltMetadataCASFixture(t, "mcas")
	defer cleanup()

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

// newDoltMetadataCASFixture composes the frozen role kit with this backend's
// accessor.
func newDoltMetadataCASFixture(t *testing.T, prefix string) (conformance.MetadataCASFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	cas, err := store.MetadataCAS()
	if err != nil {
		stop()
		t.Fatalf("MetadataCAS(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.MetadataCASFixture{
		IssuePrefix:   kit.IssuePrefix,
		MetadataCAS:   cas,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: doltCommitPending(store),
	}, ctx, stop
}
