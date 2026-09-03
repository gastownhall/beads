package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBlockingAnnotatorContract runs the BlockingAnnotator contract against the
// server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch: each Run namespaces its ids under the fixture
// prefix and names the exact ids it asks about, and WritesNothing takes a
// before/after history delta, which is only meaningful while the subtests run
// sequentially. setupTestStore already marks the PARENT parallel; no subtest
// here calls t.Parallel.
func TestBlockingAnnotatorContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltBlockingAnnotatorFixture(t, "blk")
	defer cleanup()

	t.Run("AnswersOnePerIDInRequestOrder", func(t *testing.T) {
		conformance.RunBlockingAnnotatorAnswersOnePerIDInRequestOrder(t, ctx, fixture)
	})
	t.Run("CollapsesRepeatedIDs", func(t *testing.T) {
		conformance.RunBlockingAnnotatorCollapsesRepeatedIDs(t, ctx, fixture)
	})
	t.Run("ReportsOpenBlockersOnly", func(t *testing.T) {
		conformance.RunBlockingAnnotatorReportsOpenBlockersOnly(t, ctx, fixture)
	})
	t.Run("ReportsTheInboundDirection", func(t *testing.T) {
		conformance.RunBlockingAnnotatorReportsTheInboundDirection(t, ctx, fixture)
	})
	t.Run("SeparatesParentFromBlockers", func(t *testing.T) {
		conformance.RunBlockingAnnotatorSeparatesParentFromBlockers(t, ctx, fixture)
	})
	t.Run("DropsAClosedParent", func(t *testing.T) {
		conformance.RunBlockingAnnotatorDropsAClosedParent(t, ctx, fixture)
	})
	t.Run("OrdersAndCollapsesEachList", func(t *testing.T) {
		conformance.RunBlockingAnnotatorOrdersAndCollapsesEachList(t, ctx, fixture)
	})
	t.Run("CountsAnUnresolvableBlockerAsOpen", func(t *testing.T) {
		conformance.RunBlockingAnnotatorCountsAnUnresolvableBlockerAsOpen(t, ctx, fixture)
	})
	t.Run("ReadsBothPlanes", func(t *testing.T) {
		conformance.RunBlockingAnnotatorReadsBothPlanes(t, ctx, fixture)
	})
	t.Run("IgnoresNonBlockingEdgeTypes", func(t *testing.T) {
		conformance.RunBlockingAnnotatorIgnoresNonBlockingEdgeTypes(t, ctx, fixture)
	})
	t.Run("AnnotatesAnAbsentIDBare", func(t *testing.T) {
		conformance.RunBlockingAnnotatorAnnotatesAnAbsentIDBare(t, ctx, fixture)
	})
	t.Run("ResolvesExactIDsOnly", func(t *testing.T) {
		conformance.RunBlockingAnnotatorResolvesExactIDsOnly(t, ctx, fixture)
	})
	t.Run("ReportsAtMostOneParent", func(t *testing.T) {
		conformance.RunBlockingAnnotatorReportsAtMostOneParent(t, ctx, fixture)
	})
	t.Run("AnswersAnEmptyRequest", func(t *testing.T) {
		conformance.RunBlockingAnnotatorAnswersAnEmptyRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnEmptyID", func(t *testing.T) {
		conformance.RunBlockingAnnotatorRefusesAnEmptyID(t, ctx, fixture)
	})
	t.Run("LeavesTheRequestAlone", func(t *testing.T) {
		conformance.RunBlockingAnnotatorLeavesTheRequestAlone(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunBlockingAnnotatorWritesNothing(t, ctx, fixture)
	})
}

func newDoltBlockingAnnotatorFixture(t *testing.T, prefix string) (conformance.BlockingAnnotatorFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	annotator, err := store.BlockingAnnotator()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("BlockingAnnotator(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.BlockingAnnotatorFixture{
		IssuePrefix:   kit.IssuePrefix,
		Annotator:     annotator,
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
