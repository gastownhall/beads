//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBlockingAnnotatorContract runs the BlockingAnnotator contract against the
// embedded store, which calls the SAME body the server-backed store does
// (storage/issueops.ExecuteBlockingAnnotation) and differs only in the engine
// underneath and in how the read transaction is opened. That is what this
// wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids are prefix-namespaced and every request
// names its own ids, and the history delta needs the subtests sequential
// anyway.
func TestBlockingAnnotatorContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "blk")
	ctx := t.Context()
	fixture := newEmbeddedBlockingAnnotatorFixture(t, te, "blk")

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

func newEmbeddedBlockingAnnotatorFixture(t *testing.T, te *testEnv, prefix string) conformance.BlockingAnnotatorFixture {
	t.Helper()
	annotator, err := te.store.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.BlockingAnnotatorFixture{
		IssuePrefix:   kit.IssuePrefix,
		Annotator:     annotator,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
