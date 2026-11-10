package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestEdgeReaderContract runs the EdgeReader contract against the
// server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch: each Run namespaces its ids under the fixture
// prefix and names the exact anchors it asks about, and WritesNothing takes a
// before/after history delta, which is only meaningful while the subtests run
// sequentially. setupTestStore already marks the PARENT parallel; no subtest
// here calls t.Parallel.
func TestEdgeReaderContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltEdgeReaderFixture(t, "edg")
	defer cleanup()

	t.Run("AnswersOnePerAnchorInRequestOrder", func(t *testing.T) {
		conformance.RunEdgeReaderAnswersOnePerAnchorInRequestOrder(t, ctx, fixture)
	})
	t.Run("ReportsAMissingAnchorRatherThanFailing", func(t *testing.T) {
		conformance.RunEdgeReaderReportsAMissingAnchorRatherThanFailing(t, ctx, fixture)
	})
	t.Run("DistinguishesNoEdgesFromNoAnchor", func(t *testing.T) {
		conformance.RunEdgeReaderDistinguishesNoEdgesFromNoAnchor(t, ctx, fixture)
	})
	t.Run("ReturnsTargetsVerbatim", func(t *testing.T) {
		conformance.RunEdgeReaderReturnsTargetsVerbatim(t, ctx, fixture)
	})
	t.Run("CollapsesRepeatedAnchors", func(t *testing.T) {
		conformance.RunEdgeReaderCollapsesRepeatedAnchors(t, ctx, fixture)
	})
	t.Run("OrdersEdgesByTarget", func(t *testing.T) {
		conformance.RunEdgeReaderOrdersEdgesByTarget(t, ctx, fixture)
	})
	t.Run("FiltersEdgesNotAnchors", func(t *testing.T) {
		conformance.RunEdgeReaderFiltersEdgesNotAnchors(t, ctx, fixture)
	})
	t.Run("ReadsBothPlanes", func(t *testing.T) {
		conformance.RunEdgeReaderReadsBothPlanes(t, ctx, fixture)
	})
	t.Run("ResolvesExactIDsOnly", func(t *testing.T) {
		conformance.RunEdgeReaderResolvesExactIDsOnly(t, ctx, fixture)
	})
	t.Run("AnswersAnEmptyRequest", func(t *testing.T) {
		conformance.RunEdgeReaderAnswersAnEmptyRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnEmptyID", func(t *testing.T) {
		conformance.RunEdgeReaderRefusesAnEmptyID(t, ctx, fixture)
	})
	t.Run("RefusesAnUnusableType", func(t *testing.T) {
		conformance.RunEdgeReaderRefusesAnUnusableType(t, ctx, fixture)
	})
	t.Run("LeavesTheRequestAlone", func(t *testing.T) {
		conformance.RunEdgeReaderLeavesTheRequestAlone(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunEdgeReaderWritesNothing(t, ctx, fixture)
	})
}

func newDoltEdgeReaderFixture(t *testing.T, prefix string) (conformance.EdgeReaderFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	reader, err := store.EdgeReader()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("EdgeReader(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.EdgeReaderFixture{
		IssuePrefix:   kit.IssuePrefix,
		EdgeReader:    reader,
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
