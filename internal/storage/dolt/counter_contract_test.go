package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCounterContract runs the Counter contract against the server-backed
// store.
//
// The cases are subtests of one parent so the whole role suite shares one
// store and one copy-on-write branch: each Run namespaces its ids under the
// fixture prefix and scopes its own request to them, and WritesNothing takes a
// before/after history delta, which is only meaningful while the subtests run
// sequentially. setupTestStore already marks the PARENT parallel; no subtest
// here calls t.Parallel.
func TestCounterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltCounterFixture(t, "cnt")
	defer cleanup()

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

func newDoltCounterFixture(t *testing.T, prefix string) (conformance.CounterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	counter, err := store.Counter()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("Counter(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.CounterFixture{
		IssuePrefix:  kit.IssuePrefix,
		Counter:      counter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
