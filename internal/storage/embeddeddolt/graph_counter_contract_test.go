//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestGraphCounterContract runs the GraphCounter contract against the embedded
// store. It reaches the same tx-level body the server-backed store reaches
// (issueops.ExecuteEdgeCount) and differs only in the engine underneath; that is
// what this wiring catches, and it is NOT an independent vote on the body.
//
// One environment for the whole suite. Every case seeds ids under its own prefix
// and asserts only about those, so the subtests are order-independent.
func TestGraphCounterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "gcn")
	ctx := t.Context()
	fixture := newEmbeddedGraphCounterFixture(t, te, "gcn")

	t.Run("CountsOutboundEdges", func(t *testing.T) {
		conformance.RunGraphCounterCountsOutboundEdges(t, ctx, fixture)
	})
	t.Run("CountsInboundEdges", func(t *testing.T) {
		conformance.RunGraphCounterCountsInboundEdges(t, ctx, fixture)
	})
	t.Run("AnswersOnePerAnchorInRequestOrder", func(t *testing.T) {
		conformance.RunGraphCounterAnswersOnePerAnchorInRequestOrder(t, ctx, fixture)
	})
	t.Run("DistinguishesNoEdgesFromNoAnchor", func(t *testing.T) {
		conformance.RunGraphCounterDistinguishesNoEdgesFromNoAnchor(t, ctx, fixture)
	})
	t.Run("CollapsesRepeatedAnchors", func(t *testing.T) {
		conformance.RunGraphCounterCollapsesRepeatedAnchors(t, ctx, fixture)
	})
	t.Run("FiltersEdgesNotAnchors", func(t *testing.T) {
		conformance.RunGraphCounterFiltersEdgesNotAnchors(t, ctx, fixture)
	})
	t.Run("NarrowsInboundByDependentStatus", func(t *testing.T) {
		conformance.RunGraphCounterNarrowsInboundByDependentStatus(t, ctx, fixture)
	})
	t.Run("CountsAcrossBothPlanes", func(t *testing.T) {
		conformance.RunGraphCounterCountsAcrossBothPlanes(t, ctx, fixture)
	})
	t.Run("NarrowsAWispDependentByStatus", func(t *testing.T) {
		conformance.RunGraphCounterNarrowsAWispDependentByStatus(t, ctx, fixture)
	})
	t.Run("ResolvesIDsExactly", func(t *testing.T) {
		conformance.RunGraphCounterResolvesIDsExactly(t, ctx, fixture)
	})
	t.Run("AnswersAnEmptyRequest", func(t *testing.T) {
		conformance.RunGraphCounterAnswersAnEmptyRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnUnusableRequest", func(t *testing.T) {
		conformance.RunGraphCounterRefusesAnUnusableRequest(t, ctx, fixture)
	})
	t.Run("LeavesTheRequestAlone", func(t *testing.T) {
		conformance.RunGraphCounterLeavesTheRequestAlone(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunGraphCounterWritesNothing(t, ctx, fixture)
	})
}

func newEmbeddedGraphCounterFixture(t *testing.T, te *testEnv, prefix string) conformance.GraphCounterFixture {
	t.Helper()
	counter, err := te.store.GraphCounter()
	if err != nil {
		t.Fatalf("GraphCounter(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.GraphCounterFixture{
		IssuePrefix:   kit.IssuePrefix,
		GraphCounter:  counter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
