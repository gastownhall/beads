//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCounterContract runs the Counter contract against the embedded store,
// which hands back the SAME body the server-backed store does
// (internal/workapi/storecounter) and differs only in the engine underneath.
// That is what this wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids are prefix-namespaced and every request
// is scoped to them, and the history delta needs the subtests sequential
// anyway.
func TestCounterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cnt")
	ctx := t.Context()
	fixture := newEmbeddedCounterFixture(t, te, "cnt")

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
	t.Run("PriorityBucketsCountZeroAndCountEveryRow", func(t *testing.T) {
		conformance.RunCounterPriorityBucketsCountZeroAndCountEveryRow(t, ctx, fixture)
	})
	t.Run("TheNoLabelBucketIsAbsentWhenEveryRowIsLabeled", func(t *testing.T) {
		conformance.RunCounterTheNoLabelBucketIsAbsentWhenEveryRowIsLabeled(t, ctx, fixture)
	})
	t.Run("TypeBucketsAreTheRawTypeNames", func(t *testing.T) {
		conformance.RunCounterTypeBucketsAreTheRawTypeNames(t, ctx, fixture)
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

func newEmbeddedCounterFixture(t *testing.T, te *testEnv, prefix string) conformance.CounterFixture {
	t.Helper()
	counter, err := te.store.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.CounterFixture{
		IssuePrefix:  kit.IssuePrefix,
		Counter:      counter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		CountHistory: kit.CountHistory,
	}
}
