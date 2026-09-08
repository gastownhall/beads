package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLifecycleCloseReopenContract runs the Close/Reopen half of the Lifecycle
// contract against the unit-of-work provider — the one Lifecycle implementation
// that does not share the validate/execute body the two stores share. It
// reaches the same row-level bodies through domain/db but derives Changed by
// comparing the post-state snapshot to the pre-state one instead of reading the
// row-write facts, so this is the wiring where a Changed or OpenChildren
// divergence shows up.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so the event tables are database-global and a parallel
// subtest would corrupt another subtest's count deltas.
func TestLifecycleCloseReopenContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWLifecycleCloseReopenFixture(t, ctx, "lcr")

	t.Run("CloseRefusalsCarryTheirTypesAndWriteNothing", func(t *testing.T) {
		conformance.RunLifecycleCloseRefusalsCarryTheirTypesAndWriteNothing(t, ctx, fixture)
	})
	t.Run("CloseAdmitsATransitivelyBlockedTarget", func(t *testing.T) {
		conformance.RunLifecycleCloseAdmitsATransitivelyBlockedTarget(t, ctx, fixture)
	})
	t.Run("CloseAdmitsAStaleBlockFlagWhoseBlockersHaveClosed", func(t *testing.T) {
		conformance.RunLifecycleCloseAdmitsAStaleBlockFlagWhoseBlockersHaveClosed(t, ctx, fixture)
	})
	t.Run("CloseIsIdempotentOnAClosedRowThatStillLooksBlocked", func(t *testing.T) {
		conformance.RunLifecycleCloseIsIdempotentOnAClosedRowThatStillLooksBlocked(t, ctx, fixture)
	})
	t.Run("CloseCountsOpenChildrenInBothPlanes", func(t *testing.T) {
		conformance.RunLifecycleCloseCountsOpenChildrenInBothPlanes(t, ctx, fixture)
	})
	t.Run("CloseIsIdempotentAndKeepsTheFirstClose", func(t *testing.T) {
		conformance.RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose(t, ctx, fixture)
	})
	t.Run("CloseAndReopenKeepTheClaimHolder", func(t *testing.T) {
		conformance.RunLifecycleCloseAndReopenKeepTheClaimHolder(t, ctx, fixture)
	})
	t.Run("ReopenLeavesNonDoneStatusesUnchanged", func(t *testing.T) {
		conformance.RunLifecycleReopenLeavesNonDoneStatusesUnchanged(t, ctx, fixture)
	})
	t.Run("CloseAndReopenSpanTheConfiguredDoneCategory", func(t *testing.T) {
		conformance.RunLifecycleCloseAndReopenSpanTheConfiguredDoneCategory(t, ctx, fixture)
	})
	t.Run("ExpectedVersionIsCheckedBeforeTheNoOps", func(t *testing.T) {
		conformance.RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps(t, ctx, fixture)
	})
	t.Run("ReopenRecordsItsReason", func(t *testing.T) {
		conformance.RunLifecycleReopenRecordsItsReason(t, ctx, fixture)
	})
	t.Run("ReopenProvenanceLabelsHistory", func(t *testing.T) {
		conformance.RunLifecycleReopenProvenanceLabelsHistory(t, ctx, fixture)
	})
	t.Run("ResultsAreHydratedPostStateSnapshots", func(t *testing.T) {
		conformance.RunLifecycleResultsAreHydratedPostStateSnapshots(t, ctx, fixture)
	})
	t.Run("CloseAndReopenRequireActorAndIssueID", func(t *testing.T) {
		conformance.RunLifecycleCloseAndReopenRequireActorAndIssueID(t, ctx, fixture)
	})
	t.Run("CloseSettlesItsTransitiveAndCrossPlaneDependers", func(t *testing.T) {
		conformance.RunLifecycleCloseSettlesItsTransitiveAndCrossPlaneDependers(t, ctx, fixture)
	})
	t.Run("CloseSettlesTheClosedRowItselfAndItsChild", func(t *testing.T) {
		conformance.RunLifecycleCloseSettlesTheClosedRowItselfAndItsChild(t, ctx, fixture)
	})
	t.Run("CloseOnASpawnersLastChildSatisfiesAWaitsForGate", func(t *testing.T) {
		conformance.RunLifecycleCloseOnASpawnersLastChildSatisfiesAWaitsForGate(t, ctx, fixture)
	})
	t.Run("ReopenReblocksItsDependers", func(t *testing.T) {
		conformance.RunLifecycleReopenReblocksItsDependers(t, ctx, fixture)
	})
}

func newUOWLifecycleCloseReopenFixture(t *testing.T, ctx context.Context, prefix string) conformance.LifecycleCloseReopenFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewIssueOperations: a provider that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	source, ok := provider.(IssueLifecycleSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueLifecycle accessor", provider)
	}
	lifecycle, err := source.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.LifecycleCloseReopenFixture{
		IssuePrefix:          kit.IssuePrefix,
		Lifecycle:            lifecycle,
		CreateIssue:          kit.CreateIssue,
		CreateWisp:           kit.CreateWisp,
		AddDependency:        kit.AddDependency,
		SetConfig:            kit.SetConfig,
		QueryScalar:          kit.QueryScalar,
		CountHistoryMatching: kit.CountHistoryMatching,
		// The frozen kit exposes reads only. This is the write half of the same
		// raw-SQL pass-through, inside ONE committing unit of work — which also
		// gives the whole script one session.
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return "", fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return "seed close-policy state", nil
			})
		},
	}
}
