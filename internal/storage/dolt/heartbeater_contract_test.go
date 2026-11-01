package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
)

// TestHeartbeaterContract runs the Heartbeater contract against the
// server-backed store, which wraps internal/storage/issueops.ExecuteHeartbeatInTx
// in a retrying transaction that makes no Dolt commit for a refresh and one
// for a re-arm.
func TestHeartbeaterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltHeartbeaterFixture(t, "hbc")
	defer cleanup()
	t.Run("RefreshesTheHoldersLease", func(t *testing.T) {
		conformance.RunHeartbeaterRefreshesTheHoldersLease(t, ctx, fixture)
	})
	t.Run("ChangesNothingOnTheRow", func(t *testing.T) {
		conformance.RunHeartbeaterChangesNothingOnTheRow(t, ctx, fixture)
	})
	t.Run("AGuardedWriteWithThePreHeartbeatRevisionStillWins", func(t *testing.T) {
		conformance.RunHeartbeaterAGuardedWriteWithThePreHeartbeatRevisionStillWins(t, ctx, fixture)
	})
	t.Run("RefusesAForeignHolder", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAForeignHolder(t, ctx, fixture)
	})
	t.Run("RefusesAfterClose", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterClose(t, ctx, fixture)
	})
	t.Run("RefusesAfterRelease", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterRelease(t, ctx, fixture)
	})
	t.Run("RefusesAfterReclaim", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterReclaim(t, ctx, fixture)
	})
	t.Run("RefusesAfterStatusMovedOffInProgress", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterStatusMovedOffInProgress(t, ctx, fixture)
	})
	t.Run("RefusesAWisp", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAWisp(t, ctx, fixture)
	})
	t.Run("RearmsAHandDoledClaim", func(t *testing.T) {
		conformance.RunHeartbeaterRearmsAHandDoledClaim(t, ctx, fixture)
	})
	t.Run("AnswersNotFoundForAnAbsentID", func(t *testing.T) {
		conformance.RunHeartbeaterAnswersNotFoundForAnAbsentID(t, ctx, fixture)
	})
	t.Run("RefusesABlankOrOverLongActor", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesABlankOrOverLongActor(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheRequest", func(t *testing.T) {
		conformance.RunHeartbeaterDoesNotMutateTheRequest(t, ctx, fixture)
	})
	t.Run("CommitsOnlyItsOwnTables", func(t *testing.T) {
		conformance.RunHeartbeaterCommitsOnlyItsOwnTables(t, ctx, fixture)
	})
	t.Run("ASameSecondHeartbeatIsNotARearm", func(t *testing.T) {
		conformance.RunHeartbeaterASameSecondHeartbeatIsNotARearm(t, ctx, fixture)
	})
	t.Run("RefusesAnOrphanedLeaseOnAReleasedIssue", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAReleasedIssue(t, ctx, fixture)
	})
	t.Run("RefusesAnOrphanedLeaseOnAClosedIssue", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAClosedIssue(t, ctx, fixture)
	})
	t.Run("RefusesAnOrphanedLeaseOnAnIssueSomeoneElseHolds", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAnIssueSomeoneElseHolds(t, ctx, fixture)
	})
}

// newDoltHeartbeaterFixture composes the frozen role kit with this backend's
// accessors, plus the heartbeater taken off a HookFiringStore so the quiet case
// observes the decorator a command actually holds.
func newDoltHeartbeaterFixture(t *testing.T, prefix string) (conformance.HeartbeaterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	fail := func(what string, err error) {
		stop()
		t.Fatalf("%s: %v", what, err)
	}
	heartbeater, err := store.Heartbeater()
	if err != nil {
		fail("Heartbeater()", err)
	}
	claimer, err := store.IssueClaimer()
	if err != nil {
		fail("IssueClaimer()", err)
	}
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		fail("IssueLifecycle()", err)
	}
	releaser, err := store.Releaser()
	if err != nil {
		fail("Releaser()", err)
	}
	reclaimer, err := store.LeaseReclaimer()
	if err != nil {
		fail("LeaseReclaimer()", err)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	var hooked = heartbeater
	if runner != nil {
		if hooked, err = storage.NewHookFiringStore(store, runner).Heartbeater(); err != nil {
			fail("hooked Heartbeater()", err)
		}
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.HeartbeaterFixture{
		IssuePrefix:    kit.IssuePrefix,
		Heartbeater:    heartbeater,
		CreateIssue:    kit.CreateIssue,
		CreateWisp:     kit.CreateWisp,
		Claimer:        claimer,
		Lifecycle:      lifecycle,
		Releaser:       releaser,
		LeaseReclaimer: reclaimer,
		QueryScalar:    kit.QueryScalar,
		CountHistory:   kit.CountHistory,
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			for _, stmt := range statements {
				if _, err := store.db.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
					return fmt.Errorf("%s: %w", stmt.Query, err)
				}
			}
			return nil
		},
		SetJournalEnabled:          store.SetEventsJournalEnabled,
		SetVersionedHistoryEnabled: store.SetVersionedHistoryEnabled,
		HookedHeartbeater:          hooked,
		FiredUpdates:               fired,
	}, ctx, stop
}
