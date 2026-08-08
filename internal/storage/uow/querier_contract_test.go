package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestQuerierContract runs the Querier contract against the unit-of-work
// provider — the one implementation that does not hand back
// internal/workapi/storequerier. It is the SECOND of two votes, not the third:
// the two store backends share the other body.
//
// It is also the only leg on which the Offset case takes its HONORED branch:
// this seam renders OFFSET for a filter-expressible query and skips matches in
// Go for a predicate one, where the store body refuses both uniformly.
//
// One provider for the whole suite and NO t.Parallel: this backend has no
// per-test copy-on-write branch, so dolt_log and the issues table are
// database-global and a parallel subtest would corrupt another's history delta.
func TestQuerierContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWQuerierFixture(t, ctx, "qry")

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

func newUOWQuerierFixture(t *testing.T, ctx context.Context, prefix string) conformance.QuerierFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewQuerier: a provider that stopped
	// offering the role is the regression, and a constructor call would hide it.
	source, ok := provider.(QuerierSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Querier accessor", provider)
	}
	querier, err := source.Querier()
	if err != nil {
		t.Fatalf("Querier(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.QuerierFixture{
		IssuePrefix:  kit.IssuePrefix,
		Querier:      querier,
		CreateIssue:  kit.CreateIssue,
		CountHistory: kit.CountHistory,
	}
}
