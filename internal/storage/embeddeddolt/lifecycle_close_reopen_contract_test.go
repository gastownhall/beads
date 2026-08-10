//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestLifecycleCloseReopenContract runs the Close/Reopen half of the Lifecycle
// contract against the embedded store, which shares its validate/execute body
// with the server-backed store and differs in the transaction wrapper and the
// engine underneath. That is what this wiring catches; it is not an independent
// vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids are prefix-namespaced, and the event
// deltas need the subtests sequential anyway.
func TestLifecycleCloseReopenContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "lcr")
	ctx := t.Context()
	fixture := newEmbeddedLifecycleCloseReopenFixture(t, te, "lcr")

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

func newEmbeddedLifecycleCloseReopenFixture(t *testing.T, te *testEnv, prefix string) conformance.LifecycleCloseReopenFixture {
	t.Helper()
	// Through the capability accessor, not NewIssueOperations: a store that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.LifecycleCloseReopenFixture{
		IssuePrefix:          kit.IssuePrefix,
		Lifecycle:            lifecycle,
		CreateIssue:          kit.CreateIssue,
		CreateWisp:           kit.CreateWisp,
		AddDependency:        kit.AddDependency,
		SetConfig:            kit.SetConfig,
		QueryScalar:          kit.QueryScalar,
		CountHistoryMatching: kit.CountHistoryMatching,
		// The frozen kit exposes reads only, so this is the write half of the
		// same short-lived raw connection its QueryScalar opens, pinned for the
		// whole script so a multi-statement seed stays in one session.
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
	}
}
