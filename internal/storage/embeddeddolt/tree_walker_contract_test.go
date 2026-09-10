//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestTreeWalkerContract runs the TreeWalker contract against the embedded
// store. It reaches the same tx-level body the server-backed store reaches
// (issueops.WalkDependencyTreeInTx) and differs only in the engine underneath;
// that is what this wiring catches, and it is NOT an independent vote on the
// body.
//
// One environment for the whole suite. Every case seeds ids under its own prefix
// and asserts only about those, so the subtests are order-independent.
func TestTreeWalkerContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "twk")
	ctx := t.Context()
	fixture := newEmbeddedTreeWalkerFixture(t, te, "twk")

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

func newEmbeddedTreeWalkerFixture(t *testing.T, te *testEnv, prefix string) conformance.TreeWalkerFixture {
	t.Helper()
	walker, err := te.store.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.TreeWalkerFixture{
		IssuePrefix:   kit.IssuePrefix,
		TreeWalker:    walker,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// The frozen kit exposes reads only. This is the write half of the same
		// short-lived raw connection its QueryScalar opens, and RETURNS the
		// error rather than failing the test, so the contract's own seeding
		// message is the one a reader sees. One PINNED CONNECTION for the whole
		// script.
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return err
			}
			defer func() { _ = cleanup() }()
			conn, err := db.Conn(ctx)
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
}
