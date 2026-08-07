//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/issueops"
)

// TestSweeperContract runs the Sweeper contract against the embedded store,
// which hands back the SAME body the server-backed store does
// (internal/storage/issueops.SweepInTx) and differs only in how it reaches a
// transaction and in that its commit runs outside one. That is what this
// wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, every case scopes itself to prefix-namespaced
// ids, and the history delta needs the subtests sequential anyway.
func TestSweeperContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "swp")
	ctx := t.Context()
	fixture := newEmbeddedSweeperFixture(t, te, "swp")

	t.Run("RefusesAnUnfilteredDurableSweep", func(t *testing.T) {
		conformance.RunSweeperRefusesAnUnfilteredDurableSweep(t, ctx, fixture)
	})
	t.Run("RefusesAMalformedRequest", func(t *testing.T) {
		conformance.RunSweeperRefusesAMalformedRequest(t, ctx, fixture)
	})
	t.Run("ClearsOneTierAndLeavesTheOther", func(t *testing.T) {
		conformance.RunSweeperClearsOneTierAndLeavesTheOther(t, ctx, fixture)
	})
	t.Run("ProtectsPinnedRows", func(t *testing.T) {
		conformance.RunSweeperProtectsPinnedRows(t, ctx, fixture)
	})
	t.Run("HonorsTheCutoffAndThePattern", func(t *testing.T) {
		conformance.RunSweeperHonorsTheCutoffAndThePattern(t, ctx, fixture)
	})
	t.Run("DryRunChangesNothing", func(t *testing.T) {
		conformance.RunSweeperDryRunChangesNothing(t, ctx, fixture)
	})
	t.Run("ProtectsRowsCitedFromAWispComment", func(t *testing.T) {
		conformance.RunSweeperProtectsRowsCitedFromAWispComment(t, ctx, fixture)
	})
	t.Run("ProtectsCitedRows", func(t *testing.T) {
		conformance.RunSweeperProtectsCitedRows(t, ctx, fixture)
	})
	t.Run("EmptyMatchIsZeroAndNil", func(t *testing.T) {
		conformance.RunSweeperEmptyMatchIsZeroAndNil(t, ctx, fixture)
	})
	t.Run("RecordsAtMostOneHistoryEntry", func(t *testing.T) {
		conformance.RunSweeperRecordsAtMostOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunSweeperDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

func newEmbeddedSweeperFixture(t *testing.T, te *testEnv, prefix string) conformance.SweeperFixture {
	t.Helper()
	sweeper, err := te.store.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.SweeperFixture{
		IssuePrefix:  kit.IssuePrefix,
		Sweeper:      sweeper,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
		AddComment: func(ctx context.Context, issueID, author, text string) error {
			// Through the Commenter ROLE, which resolves the plane itself, so
			// the case can cite from a wisp's comment without knowing how this
			// backend reaches wisp_comments.
			commenter, err := te.store.Commenter()
			if err != nil {
				return err
			}
			_, err = commenter.AddComment(ctx, issueops.AddCommentRequest{
				IssueID: issueID, Author: author, Text: text,
			})
			return err
		},
	}
}
