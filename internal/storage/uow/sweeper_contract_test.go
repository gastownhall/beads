package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/issueops"
)

// TestSweeperContract runs the Sweeper contract against the unit-of-work
// provider — the one implementation that does not run
// internal/storage/issueops.SweepInTx. It is the SECOND of two votes, not the
// third: the two store backends share the other body.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global
// and a parallel subtest would delete another subtest's rows — which on this
// role is not a flake but the operation working as documented.
func TestSweeperContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWSweeperFixture(t, ctx, "swp")

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

func newUOWSweeperFixture(t *testing.T, ctx context.Context, prefix string) conformance.SweeperFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewSweeper: a provider that stopped
	// offering the role is the regression a constructor call would hide.
	source, ok := provider.(SweeperSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Sweeper accessor", provider)
	}
	sweeper, err := source.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
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
			cs, ok := provider.(CommenterSource)
			if !ok {
				return fmt.Errorf("provider %T does not offer the Commenter accessor", provider)
			}
			commenter, err := cs.Commenter()
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
