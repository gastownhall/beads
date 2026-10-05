//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_graph_aux_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The per-role contract wirings for the graph and aux read roles.
//
// Every case here is the SHARED contract, unmodified: the same function the
// embedded-Dolt and unit-of-work backends run, bound to a fixture whose subject
// is the http client and whose seed hooks come off the reference store the
// server serves. A case that passes has proved the behavior ACROSS THE WIRE
// rather than proving the client agrees with itself.
//
// NOTHING IS PARKED IN THIS FILE, and that is worth saying because an earlier
// revision parked thirteen cases. The two hooks they needed — Exec, for seeding
// a cycle past every gate, and CountHistory, for the writes-nothing clause — are
// raw SQL, and holding a SQL handle open alongside the live server deadlocks the
// engine. servedEnv opens one PER CALL and closes it, which is what the embedded
// backend's own frozen fixture kit does, so both hooks are real here and every
// case runs.

func TestServedCycleDetectorContract(t *testing.T) {
	e := newServedEnv(t, "cyc")
	detector, err := e.subject.CycleDetector()
	if err != nil {
		t.Fatalf("CycleDetector(): %v", err)
	}
	fixture := conformance.CycleDetectorFixture{
		IssuePrefix:  "cyc",
		Detector:     detector,
		CreateIssue:  e.createIssue,
		CreateWisp:   e.createWisp,
		Exec:         e.exec,
		CountHistory: e.countHistory,
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.CycleDetectorFixture)
	}{
		{"ReportsNoCycleForAnAcyclicSubgraph", conformance.RunCycleDetectorReportsNoCycleForAnAcyclicSubgraph},
		{"FindsADurableCycleRotatedToItsLowestID", conformance.RunCycleDetectorFindsADurableCycleRotatedToItsLowestID},
		{"ReportsTheSameCyclesEveryRun", conformance.RunCycleDetectorReportsTheSameCyclesEveryRun},
		{"MergesTheDurableAndEphemeralPlanes", conformance.RunCycleDetectorMergesTheDurableAndEphemeralPlanes},
		{"FollowsOnlyBlockingEdges", conformance.RunCycleDetectorFollowsOnlyBlockingEdges},
		{"ReportsAnHonestPartial", conformance.RunCycleDetectorReportsAnHonestPartial},
		{"CountsAWhollyUndescribableCycle", conformance.RunCycleDetectorCountsAWhollyUndescribableCycle},
		{"WritesNothing", conformance.RunCycleDetectorWritesNothing},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}

func TestServedTreeWalkerContract(t *testing.T) {
	e := newServedEnv(t, "tre")
	walker, err := e.subject.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker(): %v", err)
	}
	fixture := conformance.TreeWalkerFixture{
		IssuePrefix:   "tre",
		TreeWalker:    walker,
		CreateIssue:   e.createIssue,
		CreateWisp:    e.createWisp,
		AddDependency: e.addDependency,
		Exec:          e.exec,
		CountHistory:  e.countHistory,
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.TreeWalkerFixture)
	}{
		{"WalksTheDependenciesOfARoot", conformance.RunTreeWalkerWalksTheDependenciesOfARoot},
		{"WalksDependentsWhenAskedUp", conformance.RunTreeWalkerWalksDependentsWhenAskedUp},
		{"BoundsTheDescentAtMaxDepth", conformance.RunTreeWalkerBoundsTheDescentAtMaxDepth},
		{"TerminatesOnACycle", conformance.RunTreeWalkerTerminatesOnACycle},
		{"RendersASharedSubtreeOnce", conformance.RunTreeWalkerRendersASharedSubtreeOnce},
		{"MergesTheDurableAndEphemeralPlanes", conformance.RunTreeWalkerMergesTheDurableAndEphemeralPlanes},
		{"FollowsEveryTypeButRelatesTo", conformance.RunTreeWalkerFollowsEveryTypeButRelatesTo},
		{"PrunesEachHalfOfABothWalk", conformance.RunTreeWalkerPrunesEachHalfOfABothWalk},
		{"AnswersBothDirectionsWithTheRootOnce", conformance.RunTreeWalkerAnswersBothDirectionsWithTheRootOnce},
		{"PrunesByStatusKeepingAncestors", conformance.RunTreeWalkerPrunesByStatusKeepingAncestors},
		{"PrunesEverythingWhenNothingMatches", conformance.RunTreeWalkerPrunesEverythingWhenNothingMatches},
		{"AnswersARootWithNoEdges", conformance.RunTreeWalkerAnswersARootWithNoEdges},
		{"RefusesAnAbsentRoot", conformance.RunTreeWalkerRefusesAnAbsentRoot},
		{"ResolvesTheRootIDExactly", conformance.RunTreeWalkerResolvesTheRootIDExactly},
		{"CrossesPlanesFromAWispRootAndUpward", conformance.RunTreeWalkerCrossesPlanesFromAWispRootAndUpward},
		{"RefusesAnInvalidRequest", conformance.RunTreeWalkerRefusesAnInvalidRequest},
		{"RefusesAWalkOverTheRowCap", conformance.RunTreeWalkerRefusesAWalkOverTheRowCap},
		{"WritesNothing", conformance.RunTreeWalkerWritesNothing},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}

func TestServedEdgeReaderContract(t *testing.T) {
	e := newServedEnv(t, "edg")
	reader, err := e.subject.EdgeReader()
	if err != nil {
		t.Fatalf("EdgeReader(): %v", err)
	}
	fixture := conformance.EdgeReaderFixture{
		IssuePrefix:   "edg",
		EdgeReader:    reader,
		CreateIssue:   e.createIssue,
		CreateWisp:    e.createWisp,
		AddDependency: e.addDependency,
		CountHistory:  e.countHistory,
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.EdgeReaderFixture)
	}{
		{"AnswersOnePerAnchorInRequestOrder", conformance.RunEdgeReaderAnswersOnePerAnchorInRequestOrder},
		{"ReportsAMissingAnchorRatherThanFailing", conformance.RunEdgeReaderReportsAMissingAnchorRatherThanFailing},
		{"DistinguishesNoEdgesFromNoAnchor", conformance.RunEdgeReaderDistinguishesNoEdgesFromNoAnchor},
		{"ReturnsTargetsVerbatim", conformance.RunEdgeReaderReturnsTargetsVerbatim},
		{"CollapsesRepeatedAnchors", conformance.RunEdgeReaderCollapsesRepeatedAnchors},
		{"OrdersEdgesByTarget", conformance.RunEdgeReaderOrdersEdgesByTarget},
		{"FiltersEdgesNotAnchors", conformance.RunEdgeReaderFiltersEdgesNotAnchors},
		{"ReadsBothPlanes", conformance.RunEdgeReaderReadsBothPlanes},
		{"ResolvesExactIDsOnly", conformance.RunEdgeReaderResolvesExactIDsOnly},
		{"AnswersAnEmptyRequest", conformance.RunEdgeReaderAnswersAnEmptyRequest},
		{"RefusesAnEmptyID", conformance.RunEdgeReaderRefusesAnEmptyID},
		{"RefusesAnUnusableType", conformance.RunEdgeReaderRefusesAnUnusableType},
		{"LeavesTheRequestAlone", conformance.RunEdgeReaderLeavesTheRequestAlone},
		{"WritesNothing", conformance.RunEdgeReaderWritesNothing},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}

func TestServedBlockingAnnotatorContract(t *testing.T) {
	e := newServedEnv(t, "blk")
	annotator, err := e.subject.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator(): %v", err)
	}
	fixture := conformance.BlockingAnnotatorFixture{
		IssuePrefix:   "blk",
		Annotator:     annotator,
		CreateIssue:   e.createIssue,
		CreateWisp:    e.createWisp,
		AddDependency: e.addDependency,
		CountHistory:  e.countHistory,
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.BlockingAnnotatorFixture)
	}{
		{"AnswersOnePerIDInRequestOrder", conformance.RunBlockingAnnotatorAnswersOnePerIDInRequestOrder},
		{"CollapsesRepeatedIDs", conformance.RunBlockingAnnotatorCollapsesRepeatedIDs},
		{"ReportsOpenBlockersOnly", conformance.RunBlockingAnnotatorReportsOpenBlockersOnly},
		{"ReportsTheInboundDirection", conformance.RunBlockingAnnotatorReportsTheInboundDirection},
		{"SeparatesParentFromBlockers", conformance.RunBlockingAnnotatorSeparatesParentFromBlockers},
		{"DropsAClosedParent", conformance.RunBlockingAnnotatorDropsAClosedParent},
		{"OrdersAndCollapsesEachList", conformance.RunBlockingAnnotatorOrdersAndCollapsesEachList},
		{"CountsAnUnresolvableBlockerAsOpen", conformance.RunBlockingAnnotatorCountsAnUnresolvableBlockerAsOpen},
		{"ReadsBothPlanes", conformance.RunBlockingAnnotatorReadsBothPlanes},
		{"IgnoresNonBlockingEdgeTypes", conformance.RunBlockingAnnotatorIgnoresNonBlockingEdgeTypes},
		{"AnnotatesAnAbsentIDBare", conformance.RunBlockingAnnotatorAnnotatesAnAbsentIDBare},
		{"ResolvesExactIDsOnly", conformance.RunBlockingAnnotatorResolvesExactIDsOnly},
		{"ReportsAtMostOneParent", conformance.RunBlockingAnnotatorReportsAtMostOneParent},
		{"AnswersAnEmptyRequest", conformance.RunBlockingAnnotatorAnswersAnEmptyRequest},
		{"RefusesAnEmptyID", conformance.RunBlockingAnnotatorRefusesAnEmptyID},
		{"LeavesTheRequestAlone", conformance.RunBlockingAnnotatorLeavesTheRequestAlone},
		{"WritesNothing", conformance.RunBlockingAnnotatorWritesNothing},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}

// TestServedStatsReporterContract runs its cases SEQUENTIALLY against one store,
// which the contract requires: the workspace-wide cases assert before/after
// DELTAS rather than absolutes, so a concurrent writer would make them read each
// other's rows.
func TestServedStatsReporterContract(t *testing.T) {
	e := newServedEnv(t, "sts")
	reporter, err := e.subject.StatsReporter()
	if err != nil {
		t.Fatalf("StatsReporter(): %v", err)
	}
	fixture := conformance.StatsReporterFixture{
		IssuePrefix:   "sts",
		StatsReporter: reporter,
		CreateIssue:   e.createIssue,
		CreateWisp:    e.createWisp,
		AddDependency: e.addDependency,
		CountHistory:  e.countHistory,
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.StatsReporterFixture)
	}{
		{"CountsEveryDurableRowByStatus", conformance.RunStatsReporterCountsEveryDurableRowByStatus},
		{"ExcludesTheWispTier", conformance.RunStatsReporterExcludesTheWispTier},
		{"AStatusOutsideTheTalliesIsCountedOnlyInTotal", conformance.RunStatsReporterAStatusOutsideTheTalliesIsCountedOnlyInTotal},
		{"BlockedCountsTheGraphNotTheStatus", conformance.RunStatsReporterBlockedCountsTheGraphNotTheStatus},
		{"BlockedExcludesByStatusNotByThePinnedFlag", conformance.RunStatsReporterBlockedExcludesByStatusNotByThePinnedFlag},
		{"BlockedCountsEveryUnfinishedStatusNotJustOpen", conformance.RunStatsReporterBlockedCountsEveryUnfinishedStatusNotJustOpen},
		{"ReadyIsOpenMinusBlocked", conformance.RunStatsReporterReadyIsOpenMinusBlocked},
		{"SkipBlockedPairsTheTwoPointers", conformance.RunStatsReporterSkipBlockedPairsTheTwoPointers},
		{"ExtendedFieldsAreAlwaysZero", conformance.RunStatsReporterExtendedFieldsAreAlwaysZero},
		{"WritesNothing", conformance.RunStatsReporterWritesNothing},
		{"AssigneeStatsScopesToOneActor", conformance.RunStatsReporterAssigneeStatsScopesToOneActor},
		{"AssigneeBlockedCountsTheStatusNotTheGraph", conformance.RunStatsReporterAssigneeBlockedCountsTheStatusNotTheGraph},
		{"AssigneeStatsMergesTheWispTier", conformance.RunStatsReporterAssigneeStatsMergesTheWispTier},
		{"AssigneeStatsPopulatesBothPointers", conformance.RunStatsReporterAssigneeStatsPopulatesBothPointers},
		{"AssigneeStatsRefusesAnEmptyAssignee", conformance.RunStatsReporterAssigneeStatsRefusesAnEmptyAssignee},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}

// THE WorkspaceConfig CONTRACT IS WIRED, and this block used to explain why it
// could not be. It is kept rather than deleted because the reason it gave was
// right, and because what retired it is not the reason it predicted.
//
// EVERY CASE IN THAT SUITE REACHES THE ROLE THROUGH SetSetting OR UnsetSetting —
// including the ones whose SUBJECT is a read, which seed the key they then read
// back, and including RefusesAnEmptyKey, which asserts ErrValidation from
// SetSetting("") itself. While settings were read-only over v0 (D8 row 11), a
// backend that cannot write could not adopt a suite whose every case writes, and
// the role was proved instead by two hand-written tests seeded PAST it plus one
// that pinned the two verbs as refusals naming themselves.
//
// The retirement path this block named was upstream's — "an accessor-only
// contract whose read cases seed out of band" — and that is NOT what happened.
// Upstream #5596 published both write operations and client wave ga-jpywb dials
// them, so the suite became adoptable WHOLE with no change to a single case: all
// twenty-one run in served_config_test.go, and the three hand-written stand-ins
// are gone, each having asserted a strict subset of what replaced it.
//
// THE LESSON WORTH KEEPING is which of the two retirement paths arrived. A
// contract this leg cannot adopt is a statement about the WIRE far more often
// than about the contract's shape, and rewriting a suite to accommodate a
// partial backend would have bought nothing that two operations did not buy
// outright. See D8 row 16's Lifecycle.Update ask, which is still open and is
// still the other kind.
