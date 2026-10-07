//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchGetterContract runs the BatchGetter contract against the embedded
// store. It reaches the same tx-level body the server-backed store reaches
// (issueops.ExecuteGetMany) and differs only in the engine underneath; that is
// what this wiring catches, and it is NOT an independent vote on the body.
//
// One environment for the whole suite. Every case seeds ids under its own
// prefix and asserts only about those, so the subtests are order-independent.
func TestBatchGetterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "bge")
	ctx := t.Context()
	fixture := newEmbeddedBatchGetterFixture(t, te, "bge")

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

func newEmbeddedBatchGetterFixture(t *testing.T, te *testEnv, prefix string) conformance.BatchGetterFixture {
	t.Helper()
	getter, err := te.store.BatchGetter()
	if err != nil {
		t.Fatalf("BatchGetter(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.BatchGetterFixture{
		IssuePrefix:  kit.IssuePrefix,
		BatchGetter:  getter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		CountHistory: kit.CountHistory,
	}
}
