package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCycleDetectorContract runs the CycleDetector contract against the
// server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. They are also SEQUENTIAL by necessity: the
// report is global, so each case's cycles stay visible to the ones after it and
// every assertion is scoped by member set. setupTestStore already marks the
// PARENT parallel; no subtest here calls t.Parallel.
func TestCycleDetectorContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltCycleDetectorFixture(t, "cyc")
	defer cleanup()

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

func newDoltCycleDetectorFixture(t *testing.T, prefix string) (conformance.CycleDetectorFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	detector, err := store.CycleDetector()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("CycleDetector(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.CycleDetectorFixture{
		IssuePrefix: kit.IssuePrefix,
		Detector:    detector,
		CreateIssue: kit.CreateIssue,
		CreateWisp:  kit.CreateWisp,
		// The frozen kit exposes reads only, so the raw write this role's cases
		// need is supplied here — over the same *sql.DB the kit's QueryScalar
		// reads through.
		//
		// One PINNED CONNECTION for the whole script: the pool would otherwise
		// hand the inserts a different session than the foreign_key_checks
		// toggle, which is the one thing the hook promises not to do.
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
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
