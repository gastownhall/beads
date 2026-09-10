//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestEdgeReaderContract runs the EdgeReader contract against the embedded
// store, which calls the SAME body the server-backed store does
// (storage/issueops.ExecuteEdgeRead) and differs only in the engine underneath
// and in how the read transaction is opened. That is what this wiring catches;
// it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids are prefix-namespaced and every request
// names its own anchors, and the history delta needs the subtests sequential
// anyway.
func TestEdgeReaderContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "edg")
	ctx := t.Context()
	fixture := newEmbeddedEdgeReaderFixture(t, te, "edg")

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

func newEmbeddedEdgeReaderFixture(t *testing.T, te *testEnv, prefix string) conformance.EdgeReaderFixture {
	t.Helper()
	reader, err := te.store.EdgeReader()
	if err != nil {
		t.Fatalf("EdgeReader(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.EdgeReaderFixture{
		IssuePrefix:   kit.IssuePrefix,
		EdgeReader:    reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
