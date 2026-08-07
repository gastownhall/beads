package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCounterContract runs the Counter contract against the unit-of-work
// provider — the one Counter implementation that does not hand back
// internal/workapi/storecounter. The two store backends share that body between
// them, which makes this the SECOND of two votes rather than the third.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global
// and a parallel subtest would corrupt another subtest's history delta.
func TestCounterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWCounterFixture(t, ctx, "cnt")

	t.Run("CountsTheDurablePlaneByDefault", func(t *testing.T) {
		conformance.RunCounterCountsTheDurablePlaneByDefault(t, ctx, fixture)
	})
	t.Run("IncludeInfraMergesTheWispTier", func(t *testing.T) {
		conformance.RunCounterIncludeInfraMergesTheWispTier(t, ctx, fixture)
	})
	t.Run("IncludeInfraExcludesGates", func(t *testing.T) {
		conformance.RunCounterIncludeInfraExcludesGates(t, ctx, fixture)
	})
	t.Run("CountsClosedRows", func(t *testing.T) {
		conformance.RunCounterCountsClosedRows(t, ctx, fixture)
	})
	t.Run("AnUnknownStatusMatchesNothing", func(t *testing.T) {
		conformance.RunCounterAnUnknownStatusMatchesNothing(t, ctx, fixture)
	})
	t.Run("GroupsPartitionTheScalarSet", func(t *testing.T) {
		conformance.RunCounterGroupsPartitionTheScalarSet(t, ctx, fixture)
	})
	t.Run("LabelBucketsOverlapSoTotalIsNotTheirSum", func(t *testing.T) {
		conformance.RunCounterLabelBucketsOverlapSoTotalIsNotTheirSum(t, ctx, fixture)
	})
	t.Run("NamesTheEmptyBuckets", func(t *testing.T) {
		conformance.RunCounterNamesTheEmptyBuckets(t, ctx, fixture)
	})
	t.Run("PrefixesPriorityBuckets", func(t *testing.T) {
		conformance.RunCounterPrefixesPriorityBuckets(t, ctx, fixture)
	})
	t.Run("RefusesAnUnknownGroup", func(t *testing.T) {
		conformance.RunCounterRefusesAnUnknownGroup(t, ctx, fixture)
	})
	t.Run("NormalizesLabelsAndLeavesTheRequestAlone", func(t *testing.T) {
		conformance.RunCounterNormalizesLabelsAndLeavesTheRequestAlone(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunCounterWritesNothing(t, ctx, fixture)
	})
}

func newUOWCounterFixture(t *testing.T, ctx context.Context, prefix string) conformance.CounterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewCounter: a provider that stopped
	// offering the role is the regression, and a constructor call would hide it.
	source, ok := provider.(CounterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Counter accessor", provider)
	}
	counter, err := source.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.CounterFixture{
		IssuePrefix:  kit.IssuePrefix,
		Counter:      counter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		CountHistory: kit.CountHistory,
	}
}
