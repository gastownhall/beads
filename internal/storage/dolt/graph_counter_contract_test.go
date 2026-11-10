package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestGraphCounterContract runs the GraphCounter contract against the
// server-backed store.
//
// It reaches the same tx-level body every other leg reaches
// (issueops.ExecuteEdgeCount), so this wiring is an ENGINE check and a wrapper
// check rather than an independent vote on the body — the contract file says so
// at the top and the cases are written for it.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. setupTestStore already marks the PARENT
// parallel and no subtest here calls t.Parallel.
func TestGraphCounterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltGraphCounterFixture(t, "gcn")
	defer cleanup()

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

func newDoltGraphCounterFixture(t *testing.T, prefix string) (conformance.GraphCounterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	counter, err := store.GraphCounter()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("GraphCounter(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.GraphCounterFixture{
		IssuePrefix:   kit.IssuePrefix,
		GraphCounter:  counter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
