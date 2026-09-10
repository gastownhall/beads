package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestStatsReporterContract runs the StatsReporter contract against the
// server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. That sharing is load-bearing here in a way it
// is not for the other roles: a summary takes no predicate, so every
// workspace-wide case asserts a BEFORE/AFTER DELTA around its own seeds, and a
// delta is only meaningful while nothing else writes to the branch between the
// two readings. setupTestStore already marks the PARENT parallel and gives it
// its own branch; no subtest here calls t.Parallel.
func TestStatsReporterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltStatsReporterFixture(t, "sts")
	defer cleanup()

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
	t.Run("BlockedCountsEveryUnfinishedStatusNotJustOpen", func(t *testing.T) {
		conformance.RunStatsReporterBlockedCountsEveryUnfinishedStatusNotJustOpen(t, ctx, fixture)
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

func newDoltStatsReporterFixture(t *testing.T, prefix string) (conformance.StatsReporterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	reporter, err := store.StatsReporter()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("StatsReporter(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.StatsReporterFixture{
		IssuePrefix:   kit.IssuePrefix,
		StatsReporter: reporter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
