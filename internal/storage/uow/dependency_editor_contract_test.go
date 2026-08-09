package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestUOWDependencyEditorContract runs the shared DependencyEditor contract
// against the unit-of-work backend.
//
// ONE PROVIDER FOR THE WHOLE SUITE, subtests over it. Each provider boots a
// real Dolt sql-server, and the contract fixtures were built for sharing —
// IssuePrefix plus a per-case id tag namespaces every row a case seeds. NO
// t.Parallel: dolt_log and the event tables are database-global here, so a
// parallel subtest would corrupt another subtest's before/after arithmetic.
func TestUOWDependencyEditorContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWDependencyEditorFixture(t, ctx)

	for _, test := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.DependencyEditorFixture)
	}{
		{name: "RoutesWispSourcedEdgeToTheWispPlane", run: conformance.RunDependencyEditorRoutesWispSourcedEdgeToTheWispPlane},
		{name: "MixedBatchWritesBothPlanes", run: conformance.RunDependencyEditorMixedBatchWritesBothPlanes},
		{name: "MixedBatchRefusalRollsBackBothPlanes", run: conformance.RunDependencyEditorMixedBatchRefusalRollsBackBothPlanes},
		{name: "RefusesCrossPlaneCycle", run: conformance.RunDependencyEditorRefusesCrossPlaneCycle},
		{name: "AddedEchoesTheRequestOrder", run: conformance.RunDependencyEditorAddedEchoesTheRequestOrder},
		{name: "SameTypeReAddIsIdempotent", run: conformance.RunDependencyEditorSameTypeReAddIsIdempotent},
		{name: "RepeatsWithinOneRequestCollapse", run: conformance.RunDependencyEditorRepeatsWithinOneRequestCollapse},
		{name: "AttributesItsEventsToTheActor", run: conformance.RunDependencyEditorAttributesItsEventsToTheActor},
		{name: "RetypeRefusalLeavesTheOriginalEdge", run: conformance.RunDependencyEditorRetypeRefusalLeavesTheOriginalEdge},
		{name: "RefusalWritesNothing", run: conformance.RunDependencyEditorRefusalWritesNothing},
		{name: "RemoveIsIdempotent", run: conformance.RunDependencyEditorRemoveIsIdempotent},
		{name: "AppliesParentChildBeforeBlockingEdges", run: conformance.RunDependencyEditorAppliesParentChildBeforeBlockingEdges},
		{name: "AcceptsAnExternalTarget", run: conformance.RunDependencyEditorAcceptsAnExternalTarget},
		{name: "AcceptsAForeignRepoTarget", run: conformance.RunDependencyEditorAcceptsAForeignRepoTarget},
		{name: "RefusesBlockingEdgeAcrossItsOwnHierarchy", run: conformance.RunDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy},
		{name: "RefusesSelfDependencyWithTheProbeSkipped", run: conformance.RunDependencyEditorRefusesSelfDependencyWithTheProbeSkipped},
		{name: "RecordsOneHistoryEntryPerLandedRequest", run: conformance.RunDependencyEditorRecordsOneHistoryEntryPerLandedRequest},
		{name: "RecordsNoHistoryForAnAllEphemeralRequest", run: conformance.RunDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest},
		{name: "SnapshotsTheRequest", run: conformance.RunDependencyEditorSnapshotsTheRequest},
		{name: "ValidationRefusalsWriteNothing", run: conformance.RunDependencyEditorValidationRefusalsWriteNothing},
		{name: "RoutesWispSourcedRemovalToTheWispPlane", run: conformance.RunDependencyEditorRoutesWispSourcedRemovalToTheWispPlane},
		{name: "RefusesAGhostSource", run: conformance.RunDependencyEditorRefusesAGhostSource},
		{name: "RefusesAMissingLocalTarget", run: conformance.RunDependencyEditorRefusesAMissingLocalTarget},
		{name: "AcceptsATypeOutsideTheConstants", run: conformance.RunDependencyEditorAcceptsATypeOutsideTheConstants},
		{name: "RemovesOnlyTheNamedEdge", run: conformance.RunDependencyEditorRemovesOnlyTheNamedEdge},
		{name: "SkipPerEdgeCycleCheckDropsOnlyTheProbe", run: conformance.RunDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe},
		{name: "RecordsOneHistoryEntryForAMixedPlaneRequest", run: conformance.RunDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest},
		{name: "WritesTheTargetIntoItsTypedColumn", run: conformance.RunDependencyEditorWritesTheTargetIntoItsTypedColumn},
		{name: "RefusesBlockingEdgeAcrossAWispHierarchy", run: conformance.RunDependencyEditorRefusesBlockingEdgeAcrossAWispHierarchy},
		{name: "RefusesACycleThroughAParentChildHop", run: conformance.RunDependencyEditorRefusesACycleThroughAParentChildHop},
		{name: "RefusesASamePlaneEdgeClosingACrossPlaneCycle", run: conformance.RunDependencyEditorRefusesASamePlaneEdgeClosingACrossPlaneCycle},
		{name: "AddMarksItsSourceInTheSameVerb", run: conformance.RunDependencyEditorAddMarksItsSourceInTheSameVerb},
		{name: "RemoveUnmarksItsSourceAndDescendants", run: conformance.RunDependencyEditorRemoveUnmarksItsSourceAndDescendants},
		{name: "MaintainsBlockedStateAcrossPlanes", run: conformance.RunDependencyEditorMaintainsBlockedStateAcrossPlanes},
		{name: "ClosedChildAddSatisfiesAnAnyChildrenGate", run: conformance.RunDependencyEditorClosedChildAddSatisfiesAnAnyChildrenGate},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, ctx, fixture)
		})
	}
}

// newUOWDependencyEditorFixture composes the backend's role fixture kit with
// the accessor under test. Every hook but the accessor comes from the kit, so
// the seeding and scalar-query plumbing stays identical to the other roles'.
func newUOWDependencyEditorFixture(t *testing.T, ctx context.Context) conformance.DependencyEditorFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, "bd")
	editor, err := NewDependencyEditor(provider)
	if err != nil {
		t.Fatalf("NewDependencyEditor: %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, "bd")
	return conformance.DependencyEditorFixture{
		IssuePrefix:   kit.IssuePrefix,
		Editor:        editor,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
	}
}
