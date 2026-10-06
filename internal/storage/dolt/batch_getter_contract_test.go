package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBatchGetterContract runs the BatchGetter contract against the
// server-backed store.
//
// It reaches the same tx-level body every other leg reaches
// (issueops.ExecuteGetMany), so this wiring is an ENGINE check and a wrapper
// check rather than an independent vote on the body — the contract file says
// so at the top and the cases are written for it.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. setupTestStore already marks the PARENT
// parallel and no subtest here calls t.Parallel.
func TestBatchGetterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltBatchGetterFixture(t, "bge")
	defer cleanup()

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

func newDoltBatchGetterFixture(t *testing.T, prefix string) (conformance.BatchGetterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	getter, err := store.BatchGetter()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("BatchGetter(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.BatchGetterFixture{
		IssuePrefix:  kit.IssuePrefix,
		BatchGetter:  getter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
