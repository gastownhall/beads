//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestCycleDetectorContract runs the CycleDetector contract against the embedded
// store. It reaches the same tx-level body the server-backed store reaches
// (issueops.DetectCycleReportInTx) and differs only in the engine underneath, so
// it is an engine check rather than an independent vote on the body.
//
// One environment for the whole suite, and the subtests are sequential: the
// report is global, so each case's cycles stay visible to the ones after it.
func TestCycleDetectorContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cyc")
	ctx := t.Context()
	fixture := newEmbeddedCycleDetectorFixture(t, te, "cyc")

	t.Run("ReportsNoCycleForAnAcyclicSubgraph", func(t *testing.T) {
		conformance.RunCycleDetectorReportsNoCycleForAnAcyclicSubgraph(t, ctx, fixture)
	})
	t.Run("FindsADurableCycleRotatedToItsLowestID", func(t *testing.T) {
		conformance.RunCycleDetectorFindsADurableCycleRotatedToItsLowestID(t, ctx, fixture)
	})
	t.Run("ReportsTheSameCyclesEveryRun", func(t *testing.T) {
		conformance.RunCycleDetectorReportsTheSameCyclesEveryRun(t, ctx, fixture)
	})
	t.Run("MergesTheDurableAndEphemeralPlanes", func(t *testing.T) {
		conformance.RunCycleDetectorMergesTheDurableAndEphemeralPlanes(t, ctx, fixture)
	})
	t.Run("FollowsOnlyBlockingEdges", func(t *testing.T) {
		conformance.RunCycleDetectorFollowsOnlyBlockingEdges(t, ctx, fixture)
	})
	t.Run("ReportsAnHonestPartial", func(t *testing.T) {
		conformance.RunCycleDetectorReportsAnHonestPartial(t, ctx, fixture)
	})
	t.Run("CountsAWhollyUndescribableCycle", func(t *testing.T) {
		conformance.RunCycleDetectorCountsAWhollyUndescribableCycle(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunCycleDetectorWritesNothing(t, ctx, fixture)
	})
}

func newEmbeddedCycleDetectorFixture(t *testing.T, te *testEnv, prefix string) conformance.CycleDetectorFixture {
	t.Helper()
	detector, err := te.store.CycleDetector()
	if err != nil {
		t.Fatalf("CycleDetector(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.CycleDetectorFixture{
		IssuePrefix: kit.IssuePrefix,
		Detector:    detector,
		CreateIssue: kit.CreateIssue,
		CreateWisp:  kit.CreateWisp,
		// The frozen kit exposes reads only, so this is the write half of the
		// same short-lived raw connection its QueryScalar opens.
		//
		// One PINNED CONNECTION for the whole script, for the reason the
		// server-backed wiring gives: a foreign_key_checks toggle and the insert
		// it is for must be the same session.
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
