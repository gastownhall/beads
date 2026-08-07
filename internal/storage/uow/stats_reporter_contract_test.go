package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestStatsReporterContract runs the StatsReporter contract against the
// unit-of-work provider — the THIRD independent body for this role, not the
// second. Both Dolt stores hand back internal/workapi/storestats, but each of
// the three spells the blocked count and the ready subtraction itself
// (domain/db/issue.go here).
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: the workspace-wide cases assert
// before/after deltas on a database this backend does not branch per test, so a
// parallel subtest would land rows inside another subtest's arithmetic.
func TestStatsReporterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWStatsReporterFixture(t, ctx, "sts")

	t.Run("CountsEveryDurableRowByStatus", func(t *testing.T) {
		conformance.RunStatsReporterCountsEveryDurableRowByStatus(t, ctx, fixture)
	})
	t.Run("ExcludesTheWispTier", func(t *testing.T) {
		conformance.RunStatsReporterExcludesTheWispTier(t, ctx, fixture)
	})
	t.Run("AStatusOutsideTheTalliesIsCountedOnlyInTotal", func(t *testing.T) {
		conformance.RunStatsReporterAStatusOutsideTheTalliesIsCountedOnlyInTotal(t, ctx, fixture)
	})
	t.Run("BlockedCountsTheGraphNotTheStatus", func(t *testing.T) {
		conformance.RunStatsReporterBlockedCountsTheGraphNotTheStatus(t, ctx, fixture)
	})
	t.Run("BlockedExcludesByStatusNotByThePinnedFlag", func(t *testing.T) {
		conformance.RunStatsReporterBlockedExcludesByStatusNotByThePinnedFlag(t, ctx, fixture)
	})
	t.Run("ReadyIsOpenMinusBlocked", func(t *testing.T) {
		conformance.RunStatsReporterReadyIsOpenMinusBlocked(t, ctx, fixture)
	})
	t.Run("SkipBlockedPairsTheTwoPointers", func(t *testing.T) {
		conformance.RunStatsReporterSkipBlockedPairsTheTwoPointers(t, ctx, fixture)
	})
	t.Run("ExtendedFieldsAreAlwaysZero", func(t *testing.T) {
		conformance.RunStatsReporterExtendedFieldsAreAlwaysZero(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunStatsReporterWritesNothing(t, ctx, fixture)
	})
	t.Run("AssigneeStatsScopesToOneActor", func(t *testing.T) {
		conformance.RunStatsReporterAssigneeStatsScopesToOneActor(t, ctx, fixture)
	})
	t.Run("AssigneeBlockedCountsTheStatusNotTheGraph", func(t *testing.T) {
		conformance.RunStatsReporterAssigneeBlockedCountsTheStatusNotTheGraph(t, ctx, fixture)
	})
	t.Run("AssigneeStatsMergesTheWispTier", func(t *testing.T) {
		conformance.RunStatsReporterAssigneeStatsMergesTheWispTier(t, ctx, fixture)
	})
	t.Run("AssigneeStatsPopulatesBothPointers", func(t *testing.T) {
		conformance.RunStatsReporterAssigneeStatsPopulatesBothPointers(t, ctx, fixture)
	})
	t.Run("AssigneeStatsRefusesAnEmptyAssignee", func(t *testing.T) {
		conformance.RunStatsReporterAssigneeStatsRefusesAnEmptyAssignee(t, ctx, fixture)
	})
}

func newUOWStatsReporterFixture(t *testing.T, ctx context.Context, prefix string) conformance.StatsReporterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewStatsReporter: a provider that
	// stopped offering the role is the regression.
	source, ok := provider.(StatsReporterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the StatsReporter accessor", provider)
	}
	reporter, err := source.StatsReporter()
	if err != nil {
		t.Fatalf("StatsReporter(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.StatsReporterFixture{
		IssuePrefix:   kit.IssuePrefix,
		StatsReporter: reporter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
