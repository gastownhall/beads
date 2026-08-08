//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestQuerierContract runs the Querier contract against the embedded store,
// which hands back the SAME body the server-backed store does
// (internal/workapi/storequerier) and differs only in the engine underneath.
// That is what this wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids and labels are prefix-namespaced and
// every EXPRESSION is scoped to them, and the history delta needs the subtests
// sequential anyway.
func TestQuerierContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "qry")
	ctx := t.Context()
	fixture := newEmbeddedQuerierFixture(t, te, "qry")

	t.Run("DisjunctionAnswersEveryMatch", func(t *testing.T) {
		conformance.RunQuerierDisjunctionAnswersEveryMatch(t, ctx, fixture)
	})
	t.Run("PageIsAPrefixAndHasMoreIsExact", func(t *testing.T) {
		conformance.RunQuerierPageIsAPrefixAndHasMoreIsExact(t, ctx, fixture)
	})
	t.Run("SortBoundsThePageInOrder", func(t *testing.T) {
		conformance.RunQuerierSortBoundsThePageInOrder(t, ctx, fixture)
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

func newEmbeddedQuerierFixture(t *testing.T, te *testEnv, prefix string) conformance.QuerierFixture {
	t.Helper()
	querier, err := te.store.Querier()
	if err != nil {
		t.Fatalf("Querier(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.QuerierFixture{
		IssuePrefix:  kit.IssuePrefix,
		Querier:      querier,
		CreateIssue:  kit.CreateIssue,
		CountHistory: kit.CountHistory,
	}
}
