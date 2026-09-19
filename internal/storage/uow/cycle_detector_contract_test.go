package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCycleDetectorContract runs the CycleDetector contract against the
// unit-of-work provider — the one implementation that does not wrap the store's
// own transaction. The two store backends share their five-line body, which
// makes this the SECOND of two votes rather than the third.
//
// One provider for the whole suite and NO t.Parallel: this backend has no
// per-test copy-on-write branch, so dolt_log and the dependency tables are
// database-global — and the report is global anyway, which is why every case
// scopes itself by member set.
func TestCycleDetectorContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWCycleDetectorFixture(t, ctx, "cyc")

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

func newUOWCycleDetectorFixture(t *testing.T, ctx context.Context, prefix string) conformance.CycleDetectorFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewCycleDetector: a provider that
	// stopped offering the role is the regression a constructor call would hide.
	source, ok := provider.(CycleDetectorSource)
	if !ok {
		t.Fatalf("provider %T does not offer the CycleDetector accessor", provider)
	}
	detector, err := source.CycleDetector()
	if err != nil {
		t.Fatalf("CycleDetector(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.CycleDetectorFixture{
		IssuePrefix: kit.IssuePrefix,
		Detector:    detector,
		CreateIssue: kit.CreateIssue,
		CreateWisp:  kit.CreateWisp,
		// The frozen kit exposes reads only. This is the write half of the same
		// raw-SQL pass-through, inside ONE committing unit of work — which also
		// gives the whole script one session, so a foreign_key_checks toggle
		// covers the inserts it was written for.
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return "", fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return "seed cycle edges", nil
			})
		},
		CountHistory: kit.CountHistory,
	}
}
