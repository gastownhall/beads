//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestStatsReporterContract runs the StatsReporter contract against the
// embedded store.
//
// UNLIKE THE OTHER ROLE WIRINGS IN THIS PACKAGE, this one is a genuine second
// vote rather than an engine check. The two stores share the role body
// (internal/workapi/storestats), but the statistics QUERIES underneath are this
// package's own: embeddeddolt/statistics.go writes its own blocked count and
// ready subtraction. Only the status tally is shared code.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, and the delta assertions need the subtests
// sequential anyway.
func TestStatsReporterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "sts")
	ctx := t.Context()
	fixture := newEmbeddedStatsReporterFixture(t, te, "sts")

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

func newEmbeddedStatsReporterFixture(t *testing.T, te *testEnv, prefix string) conformance.StatsReporterFixture {
	t.Helper()
	reporter, err := te.store.StatsReporter()
	if err != nil {
		t.Fatalf("StatsReporter(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.StatsReporterFixture{
		IssuePrefix:   kit.IssuePrefix,
		StatsReporter: reporter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
