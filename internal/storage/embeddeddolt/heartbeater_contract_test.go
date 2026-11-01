//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestHeartbeaterContract runs the Heartbeater contract against the embedded
// store, which wraps internal/storage/issueops.ExecuteHeartbeatInTx in one
// connection's transaction and records no version entry after a refresh, one
// after a re-arm.
func TestHeartbeaterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "hbc")
	ctx := t.Context()
	fixture := newEmbeddedHeartbeaterFixture(t, te, "hbc")
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

func newEmbeddedHeartbeaterFixture(t *testing.T, te *testEnv, prefix string) conformance.HeartbeaterFixture {
	t.Helper()
	heartbeater, err := te.store.Heartbeater()
	if err != nil {
		t.Fatalf("Heartbeater(): %v", err)
	}
	claimer, err := te.store.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	releaser, err := te.store.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	reclaimer, err := te.store.LeaseReclaimer()
	if err != nil {
		t.Fatalf("LeaseReclaimer(): %v", err)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	hooked := heartbeater
	if runner != nil {
		if hooked, err = storage.NewHookFiringStore(te.store, runner).Heartbeater(); err != nil {
			t.Fatalf("hooked Heartbeater(): %v", err)
		}
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
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
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return err
			}
			defer func() { _ = cleanup() }()
			for _, stmt := range statements {
				if _, err := db.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
					return fmt.Errorf("%s: %w", stmt.Query, err)
				}
			}
			return nil
		},
		SetJournalEnabled:          te.store.SetEventsJournalEnabled,
		SetVersionedHistoryEnabled: te.store.SetVersionedHistoryEnabled,
		HookedHeartbeater:          hooked,
		FiredUpdates:               fired,
	}
}
