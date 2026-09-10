package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestTreeWalkerContract runs the TreeWalker contract against the unit-of-work
// provider.
//
// For most roles this is the wiring where a genuine seam divergence shows up,
// because the unit of work is a second body. NOT FOR THIS ROLE: it reaches the
// same issueops.WalkDependencyTreeInTx through the domain repository, so what
// this leg checks is the WRAPPER — that the request survives the trip and that
// the typed refusals still match errors.Is/errors.As after crossing two layers
// whose siblings wrap their errors.
//
// One provider for the whole suite and NO t.Parallel: this backend has no
// per-test copy-on-write branch, so the tables are database-global. Every case
// scopes itself by the ids it seeded.
func TestTreeWalkerContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWTreeWalkerFixture(t, ctx, "twk")

	t.Run("WalksTheDependenciesOfARoot", func(t *testing.T) {
		conformance.RunTreeWalkerWalksTheDependenciesOfARoot(t, ctx, fixture)
	})
	t.Run("WalksDependentsWhenAskedUp", func(t *testing.T) {
		conformance.RunTreeWalkerWalksDependentsWhenAskedUp(t, ctx, fixture)
	})
	t.Run("BoundsTheDescentAtMaxDepth", func(t *testing.T) {
		conformance.RunTreeWalkerBoundsTheDescentAtMaxDepth(t, ctx, fixture)
	})
	t.Run("TerminatesOnACycle", func(t *testing.T) {
		conformance.RunTreeWalkerTerminatesOnACycle(t, ctx, fixture)
	})
	t.Run("RendersASharedSubtreeOnce", func(t *testing.T) {
		conformance.RunTreeWalkerRendersASharedSubtreeOnce(t, ctx, fixture)
	})
	t.Run("MergesTheDurableAndEphemeralPlanes", func(t *testing.T) {
		conformance.RunTreeWalkerMergesTheDurableAndEphemeralPlanes(t, ctx, fixture)
	})
	t.Run("FollowsEveryTypeButRelatesTo", func(t *testing.T) {
		conformance.RunTreeWalkerFollowsEveryTypeButRelatesTo(t, ctx, fixture)
	})
	t.Run("PrunesEachHalfOfABothWalk", func(t *testing.T) {
		conformance.RunTreeWalkerPrunesEachHalfOfABothWalk(t, ctx, fixture)
	})
	t.Run("AnswersBothDirectionsWithTheRootOnce", func(t *testing.T) {
		conformance.RunTreeWalkerAnswersBothDirectionsWithTheRootOnce(t, ctx, fixture)
	})
	t.Run("PrunesByStatusKeepingAncestors", func(t *testing.T) {
		conformance.RunTreeWalkerPrunesByStatusKeepingAncestors(t, ctx, fixture)
	})
	t.Run("PrunesEverythingWhenNothingMatches", func(t *testing.T) {
		conformance.RunTreeWalkerPrunesEverythingWhenNothingMatches(t, ctx, fixture)
	})
	t.Run("AnswersARootWithNoEdges", func(t *testing.T) {
		conformance.RunTreeWalkerAnswersARootWithNoEdges(t, ctx, fixture)
	})
	t.Run("RefusesAnAbsentRoot", func(t *testing.T) {
		conformance.RunTreeWalkerRefusesAnAbsentRoot(t, ctx, fixture)
	})
	t.Run("ResolvesTheRootIDExactly", func(t *testing.T) {
		conformance.RunTreeWalkerResolvesTheRootIDExactly(t, ctx, fixture)
	})
	t.Run("CrossesPlanesFromAWispRootAndUpward", func(t *testing.T) {
		conformance.RunTreeWalkerCrossesPlanesFromAWispRootAndUpward(t, ctx, fixture)
	})
	t.Run("RefusesAnInvalidRequest", func(t *testing.T) {
		conformance.RunTreeWalkerRefusesAnInvalidRequest(t, ctx, fixture)
	})
	t.Run("RefusesAWalkOverTheRowCap", func(t *testing.T) {
		conformance.RunTreeWalkerRefusesAWalkOverTheRowCap(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunTreeWalkerWritesNothing(t, ctx, fixture)
	})
}

func newUOWTreeWalkerFixture(t *testing.T, ctx context.Context, prefix string) conformance.TreeWalkerFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewTreeWalker: a provider that stopped
	// offering the role is the regression, and a constructor call would hide it.
	source, ok := provider.(TreeWalkerSource)
	if !ok {
		t.Fatalf("provider %T does not offer the TreeWalker accessor", provider)
	}
	walker, err := source.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.TreeWalkerFixture{
		IssuePrefix:   kit.IssuePrefix,
		TreeWalker:    walker,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// The frozen kit exposes reads only. This is the write half of the same
		// unfiltered raw-SQL pass-through its QueryScalar reads through, inside
		// ONE committing unit of work, which gives the whole script one session.
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return "", fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return "seed tree walk edges", nil
			})
		},
		CountHistory: kit.CountHistory,
	}
}
