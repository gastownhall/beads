package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestQuerierContract runs the Querier contract against the server-backed
// store, which reaches the shared body (internal/workapi/storequerier) and the
// seam that renders LIMIT without OFFSET.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch: each Run scopes its own EXPRESSION to a label
// under the fixture prefix, and WritesNothing takes a before/after history
// delta, which is only meaningful while the subtests run sequentially.
// setupTestStore already marks the PARENT parallel; no subtest calls t.Parallel.
func TestQuerierContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltQuerierFixture(t, "qry")
	defer cleanup()

	t.Run("DisjunctionAnswersEveryMatch", func(t *testing.T) {
		conformance.RunQuerierDisjunctionAnswersEveryMatch(t, ctx, fixture)
	})
	t.Run("PageIsAPrefixAndHasMoreIsExact", func(t *testing.T) {
		conformance.RunQuerierPageIsAPrefixAndHasMoreIsExact(t, ctx, fixture)
	})
	t.Run("SortBoundsThePageInOrder", func(t *testing.T) {
		conformance.RunQuerierSortBoundsThePageInOrder(t, ctx, fixture)
	})
	t.Run("SortByTitleFoldsCaseBeforeItCutsThePage", func(t *testing.T) {
		conformance.RunQuerierSortByTitleFoldsCaseBeforeItCutsThePage(t, ctx, fixture)
	})
	t.Run("SortByClosedPutsTheUnclosedRowsAtTheFarEnd", func(t *testing.T) {
		conformance.RunQuerierSortByClosedPutsTheUnclosedRowsAtTheFarEnd(t, ctx, fixture)
	})
	t.Run("SortTieBreaksByIDInBothDirections", func(t *testing.T) {
		conformance.RunQuerierSortTieBreaksByIDInBothDirections(t, ctx, fixture)
	})
	t.Run("SortSeesTheWholeMatchingSet", func(t *testing.T) {
		conformance.RunQuerierSortSeesTheWholeMatchingSet(t, ctx, fixture)
	})
	t.Run("HidesClosedUnlessTheExpressionOrTheFlagSaysOtherwise", func(t *testing.T) {
		conformance.RunQuerierHidesClosedUnlessTheExpressionOrTheFlagSaysOtherwise(t, ctx, fixture)
	})
	t.Run("RefusesAMalformedRequest", func(t *testing.T) {
		conformance.RunQuerierRefusesAMalformedRequest(t, ctx, fixture)
	})
	t.Run("OffsetSkipsMatches", func(t *testing.T) {
		conformance.RunQuerierOffsetSkipsMatches(t, ctx, fixture)
	})
	t.Run("EmptyMatchIsAWellFormedPage", func(t *testing.T) {
		conformance.RunQuerierEmptyMatchIsAWellFormedPage(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunQuerierWritesNothing(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunQuerierDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// newDoltQuerierFixture composes the frozen role kit with this backend's
// accessor.
func newDoltQuerierFixture(t *testing.T, prefix string) (conformance.QuerierFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	querier, err := store.Querier()
	if err != nil {
		stop()
		t.Fatalf("Querier(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.QuerierFixture{
		IssuePrefix:  kit.IssuePrefix,
		Querier:      querier,
		CreateIssue:  kit.CreateIssue,
		CountHistory: kit.CountHistory,
	}, ctx, stop
}
