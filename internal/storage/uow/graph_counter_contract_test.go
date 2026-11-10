package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestGraphCounterContract runs the GraphCounter contract against the
// unit-of-work provider.
//
// For most roles this is the wiring where a genuine seam divergence shows up,
// because the unit of work is a second body. NOT FOR THIS ROLE: it reaches the
// same issueops.ExecuteEdgeCount through the domain repository, whose runner
// publishes exactly the DBTX method set that function takes. What this leg
// checks is the WRAPPER — that the request survives the trip and that
// ErrValidation still matches errors.Is after crossing two layers whose
// siblings wrap their errors.
//
// One provider for the whole suite and NO t.Parallel: this backend has no
// per-test copy-on-write branch, so the tables are database-global. Every case
// scopes itself by the ids it seeded.
func TestGraphCounterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWGraphCounterFixture(t, ctx, "gcn")

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

func newUOWGraphCounterFixture(t *testing.T, ctx context.Context, prefix string) conformance.GraphCounterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewGraphCounter: a provider that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	source, ok := provider.(GraphCounterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the GraphCounter accessor", provider)
	}
	counter, err := source.GraphCounter()
	if err != nil {
		t.Fatalf("GraphCounter(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.GraphCounterFixture{
		IssuePrefix:   kit.IssuePrefix,
		GraphCounter:  counter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
