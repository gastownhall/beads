package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMetadataCASContract runs the MetadataCAS contract against the
// unit-of-work provider, which reaches the same
// internal/storage/issueops.CompareAndSetMetadataKeyInTx the two store backends
// wrap — through the domain issue repository rather than through a store
// accessor.
//
// So this is the third wrapper over ONE body, not a third vote. What it can
// still catch is this leg's own wrapper: a request field dropped between the
// role and the use case, a commit message composed for a swap that wrote
// nothing, a refusal that stops matching errors.Is on the way back up.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global and
// the history deltas are only meaningful while the subtests run sequentially.
func TestMetadataCASContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWMetadataCASFixture(t, ctx, "mcas")

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

func newUOWMetadataCASFixture(t *testing.T, ctx context.Context, prefix string) conformance.MetadataCASFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewMetadataCAS: a provider that
	// stopped offering the role is the regression a constructor call would hide.
	source, ok := provider.(MetadataCASSource)
	if !ok {
		t.Fatalf("provider %T does not offer the MetadataCAS accessor", provider)
	}
	cas, err := source.MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.MetadataCASFixture{
		IssuePrefix:   kit.IssuePrefix,
		MetadataCAS:   cas,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: uowCommitPending(provider),
	}
}
