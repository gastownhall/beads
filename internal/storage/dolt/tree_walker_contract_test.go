package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestTreeWalkerContract runs the TreeWalker contract against the server-backed
// store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. setupTestStore already marks the PARENT
// parallel and no subtest here calls t.Parallel.
func TestTreeWalkerContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltTreeWalkerFixture(t, "twk")
	defer cleanup()

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

func newDoltTreeWalkerFixture(t *testing.T, prefix string) (conformance.TreeWalkerFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	walker, err := store.TreeWalker()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("TreeWalker(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.TreeWalkerFixture{
		IssuePrefix:   kit.IssuePrefix,
		TreeWalker:    walker,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// The frozen kit exposes reads only, so the raw write the cycle case
		// needs is supplied here — over the same *sql.DB the kit's QueryScalar
		// reads through. One PINNED CONNECTION for the whole script, so a
		// multi-statement seed cannot be split across sessions.
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			conn, err := store.db.Conn(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			for _, stmt := range statements {
				if _, err := conn.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
					return fmt.Errorf("%s: %w", stmt.Query, err)
				}
			}
			return nil
		},
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
