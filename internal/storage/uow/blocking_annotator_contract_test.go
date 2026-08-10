package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBlockingAnnotatorContract runs the BlockingAnnotator contract against the
// unit-of-work provider — the one implementation that does not call
// storage/issueops.ExecuteBlockingAnnotation, so this is the wiring where a
// genuine body divergence shows up. It is the SECOND of two votes, not the
// third.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global
// and a parallel subtest would corrupt another subtest's history delta.
func TestBlockingAnnotatorContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWBlockingAnnotatorFixture(t, ctx, "blk")

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

func newUOWBlockingAnnotatorFixture(t *testing.T, ctx context.Context, prefix string) conformance.BlockingAnnotatorFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewBlockingAnnotator: a provider that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	source, ok := provider.(BlockingAnnotatorSource)
	if !ok {
		t.Fatalf("provider %T does not offer the BlockingAnnotator accessor", provider)
	}
	annotator, err := source.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.BlockingAnnotatorFixture{
		IssuePrefix:   kit.IssuePrefix,
		Annotator:     annotator,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
