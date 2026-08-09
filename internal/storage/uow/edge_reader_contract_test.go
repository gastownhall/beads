package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestEdgeReaderContract runs the EdgeReader contract against the unit-of-work
// provider — the one EdgeReader implementation that does not call
// storage/issueops.ExecuteEdgeRead, so this is the wiring where a genuine body
// divergence shows up. It probes anchor existence through two batched use-case
// reads, one per plane, where the two store backends run one batched EXISTS over
// both tables; that makes this the SECOND of two votes rather than the third.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so a parallel subtest would corrupt another subtest's
// history delta.
func TestEdgeReaderContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWEdgeReaderFixture(t, ctx, "edg")

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

func newUOWEdgeReaderFixture(t *testing.T, ctx context.Context, prefix string) conformance.EdgeReaderFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewEdgeReader: a provider that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	source, ok := provider.(EdgeReaderSource)
	if !ok {
		t.Fatalf("provider %T does not offer the EdgeReader accessor", provider)
	}
	reader, err := source.EdgeReader()
	if err != nil {
		t.Fatalf("EdgeReader(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.EdgeReaderFixture{
		IssuePrefix:   kit.IssuePrefix,
		EdgeReader:    reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
