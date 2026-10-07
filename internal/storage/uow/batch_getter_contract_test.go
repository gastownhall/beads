package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchGetterContract runs the BatchGetter contract against the
// unit-of-work provider.
//
// For most roles this is the wiring where a genuine seam divergence shows up,
// because the unit of work is a second body. NOT FOR THIS ROLE: it reaches the
// same issueops.ExecuteGetMany through the domain repository, whose runner
// publishes exactly the DBTX method set that function takes. What this leg
// checks is the WRAPPER — that the request survives the trip and that
// ErrValidation still matches errors.Is after crossing two layers whose
// siblings wrap their errors.
//
// One provider for the whole suite and NO t.Parallel: this backend has no
// per-test copy-on-write branch, so the tables are database-global. Every case
// scopes itself by the ids it seeded.
func TestBatchGetterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWBatchGetterFixture(t, ctx, "bge")

	t.Run("FindsRequestedIssues", func(t *testing.T) {
		conformance.RunBatchGetterFindsRequestedIssues(t, ctx, fixture)
	})
	t.Run("ReportsMissingIDs", func(t *testing.T) {
		conformance.RunBatchGetterReportsMissingIDs(t, ctx, fixture)
	})
	t.Run("CollapsesRepeatedIDs", func(t *testing.T) {
		conformance.RunBatchGetterCollapsesRepeatedIDs(t, ctx, fixture)
	})
	t.Run("AnswersInRequestOrder", func(t *testing.T) {
		conformance.RunBatchGetterAnswersInRequestOrder(t, ctx, fixture)
	})
	t.Run("ResolvesIDsExactly", func(t *testing.T) {
		conformance.RunBatchGetterResolvesIDsExactly(t, ctx, fixture)
	})
	t.Run("AnswersAnEmptyRequest", func(t *testing.T) {
		conformance.RunBatchGetterAnswersAnEmptyRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnUnusableRequest", func(t *testing.T) {
		conformance.RunBatchGetterRefusesAnUnusableRequest(t, ctx, fixture)
	})
	t.Run("RefusesOverTheCap", func(t *testing.T) {
		conformance.RunBatchGetterRefusesOverTheCap(t, ctx, fixture)
	})
	t.Run("AcceptsExactlyTheCap", func(t *testing.T) {
		conformance.RunBatchGetterAcceptsExactlyTheCap(t, ctx, fixture)
	})
	t.Run("LeavesTheRequestAlone", func(t *testing.T) {
		conformance.RunBatchGetterLeavesTheRequestAlone(t, ctx, fixture)
	})
	t.Run("SharesOneReadStructurally", func(t *testing.T) {
		conformance.RunBatchGetterSharesOneReadStructurally(t, ctx, fixture)
	})
	t.Run("HydratesLabels", func(t *testing.T) {
		conformance.RunBatchGetterHydratesLabels(t, ctx, fixture)
	})
	t.Run("CrossesBothPlanes", func(t *testing.T) {
		conformance.RunBatchGetterCrossesBothPlanes(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunBatchGetterWritesNothing(t, ctx, fixture)
	})
}

func newUOWBatchGetterFixture(t *testing.T, ctx context.Context, prefix string) conformance.BatchGetterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewBatchGetter: a provider that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	source, ok := provider.(BatchGetterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the BatchGetter accessor", provider)
	}
	getter, err := source.BatchGetter()
	if err != nil {
		t.Fatalf("BatchGetter(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.BatchGetterFixture{
		IssuePrefix:  kit.IssuePrefix,
		BatchGetter:  getter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		CountHistory: kit.CountHistory,
	}
}
