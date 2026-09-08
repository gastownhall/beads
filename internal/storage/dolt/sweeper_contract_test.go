package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/issueops"
)

// TestSweeperContract runs the Sweeper contract against the server-backed
// store, which reaches internal/storage/issueops.SweepInTx through its own
// write transaction and is the one wiring whose version-control entry is
// recorded INSIDE that transaction; the other two publish theirs after it.
//
// The cases are subtests of one parent so the whole role suite shares one
// store and one copy-on-write branch. setupTestStore already marks the PARENT
// parallel; no subtest here calls t.Parallel, and RecordsExactlyOneHistoryEntry
// takes a before/after delta that is only meaningful while they run
// sequentially.
func TestSweeperContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltSweeperFixture(t, "swp")
	defer cleanup()

	t.Run("RefusesAnUnfilteredDurableSweep", func(t *testing.T) {
		conformance.RunSweeperRefusesAnUnfilteredDurableSweep(t, ctx, fixture)
	})
	t.Run("RefusesAMalformedRequest", func(t *testing.T) {
		conformance.RunSweeperRefusesAMalformedRequest(t, ctx, fixture)
	})
	t.Run("ClearsOneTierAndLeavesTheOther", func(t *testing.T) {
		conformance.RunSweeperClearsOneTierAndLeavesTheOther(t, ctx, fixture)
	})
	t.Run("TreatsALegacyTypedWispAsEphemeralTier", func(t *testing.T) {
		conformance.RunSweeperTreatsALegacyTypedWispAsEphemeralTier(t, ctx, fixture)
	})
	t.Run("LeavesNoHistoryBeadsToTheDurableTier", func(t *testing.T) {
		conformance.RunSweeperLeavesNoHistoryBeadsToTheDurableTier(t, ctx, fixture)
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
	t.Run("RecordsExactlyOneHistoryEntry", func(t *testing.T) {
		conformance.RunSweeperRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunSweeperDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// newDoltSweeperFixture composes the frozen role kit with this backend's
// accessor.
func newDoltSweeperFixture(t *testing.T, prefix string) (conformance.SweeperFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	sweeper, err := store.Sweeper()
	if err != nil {
		stop()
		t.Fatalf("Sweeper(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.SweeperFixture{
		IssuePrefix:   kit.IssuePrefix,
		Sweeper:       sweeper,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: doltCommitPending(store),
		// The write half of the same *sql.DB the kit's QueryScalar reads
		// through, for the case that manufactures a legacy row shape.
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			for _, stmt := range statements {
				if _, err := store.db.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
					return fmt.Errorf("%s: %w", stmt.Query, err)
				}
			}
			return nil
		},
		AddComment: func(ctx context.Context, issueID, author, text string) error {
			// Through the Commenter ROLE, which resolves the plane itself, so
			// the case can cite from a wisp's comment without knowing how this
			// backend reaches wisp_comments.
			commenter, err := store.Commenter()
			if err != nil {
				return err
			}
			_, err = commenter.AddComment(ctx, issueops.AddCommentRequest{
				IssueID: issueID, Author: author, Text: text,
			})
			return err
		},
	}, ctx, stop
}
