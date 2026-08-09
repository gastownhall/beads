package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLifecycleCloseReopenContract runs the Close/Reopen half of the Lifecycle
// contract against the server-backed store. It shares its validate/execute body
// with the embedded store (internal/storage/issueops ExecuteClose and
// ExecuteReopen), so this wiring and the embedded one are ONE vote on the
// semantics; the unit-of-work wiring is the second.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch: each Run namespaces its ids under the fixture
// prefix, and the event assertions take before/after deltas, which is only
// meaningful while the subtests run sequentially. setupTestStore already marks
// the PARENT parallel; no subtest here calls t.Parallel.
func TestLifecycleCloseReopenContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltLifecycleCloseReopenFixture(t, "lcr")
	defer cleanup()

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

func newDoltLifecycleCloseReopenFixture(t *testing.T, prefix string) (conformance.LifecycleCloseReopenFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the capability accessor, not NewIssueOperations: a store that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.LifecycleCloseReopenFixture{
		IssuePrefix:          kit.IssuePrefix,
		Lifecycle:            lifecycle,
		CreateIssue:          kit.CreateIssue,
		CreateWisp:           kit.CreateWisp,
		AddDependency:        kit.AddDependency,
		SetConfig:            kit.SetConfig,
		QueryScalar:          kit.QueryScalar,
		CountHistoryMatching: kit.CountHistoryMatching,
		// The frozen kit exposes reads only, so the raw writes the close-policy
		// cases need are supplied here — over the same *sql.DB its QueryScalar
		// reads through, on ONE PINNED CONNECTION so a multi-statement seed
		// cannot be split across sessions.
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
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
