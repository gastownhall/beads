package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestDependencyEditorRoutesWispSourcedEdgeToTheWispPlane(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "wroute")
	defer cleanup()
	conformance.RunDependencyEditorRoutesWispSourcedEdgeToTheWispPlane(t, ctx, fixture)
}

func TestDependencyEditorMixedBatchWritesBothPlanes(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "wmixed")
	defer cleanup()
	conformance.RunDependencyEditorMixedBatchWritesBothPlanes(t, ctx, fixture)
}

func TestDependencyEditorMixedBatchRefusalRollsBackBothPlanes(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "wrollback")
	defer cleanup()
	conformance.RunDependencyEditorMixedBatchRefusalRollsBackBothPlanes(t, ctx, fixture)
}

func TestDependencyEditorRefusesCrossPlaneCycle(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "wcycle")
	defer cleanup()
	conformance.RunDependencyEditorRefusesCrossPlaneCycle(t, ctx, fixture)
}

func TestDependencyEditorAddedEchoesTheRequestOrder(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "echo")
	defer cleanup()
	conformance.RunDependencyEditorAddedEchoesTheRequestOrder(t, ctx, fixture)
}

func TestDependencyEditorSameTypeReAddIsIdempotent(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "idem")
	defer cleanup()
	conformance.RunDependencyEditorSameTypeReAddIsIdempotent(t, ctx, fixture)
}

func TestDependencyEditorRepeatsWithinOneRequestCollapse(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "repeat")
	defer cleanup()
	conformance.RunDependencyEditorRepeatsWithinOneRequestCollapse(t, ctx, fixture)
}

func TestDependencyEditorAttributesItsEventsToTheActor(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "depactor")
	defer cleanup()
	conformance.RunDependencyEditorAttributesItsEventsToTheActor(t, ctx, fixture)
}

func TestDependencyEditorRetypeRefusalLeavesTheOriginalEdge(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "retype")
	defer cleanup()
	conformance.RunDependencyEditorRetypeRefusalLeavesTheOriginalEdge(t, ctx, fixture)
}

func TestDependencyEditorRefusalWritesNothing(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "refuse")
	defer cleanup()
	conformance.RunDependencyEditorRefusalWritesNothing(t, ctx, fixture)
}

func TestDependencyEditorRemoveIsIdempotent(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "rm")
	defer cleanup()
	conformance.RunDependencyEditorRemoveIsIdempotent(t, ctx, fixture)
}

func TestDependencyEditorAppliesParentChildBeforeBlockingEdges(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "pcfirst")
	defer cleanup()
	conformance.RunDependencyEditorAppliesParentChildBeforeBlockingEdges(t, ctx, fixture)
}

func TestDependencyEditorAcceptsAnExternalTarget(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "ext")
	defer cleanup()
	conformance.RunDependencyEditorAcceptsAnExternalTarget(t, ctx, fixture)
}

func TestDependencyEditorAcceptsAForeignRepoTarget(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "foreign")
	defer cleanup()
	conformance.RunDependencyEditorAcceptsAForeignRepoTarget(t, ctx, fixture)
}

func TestDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "hier")
	defer cleanup()
	conformance.RunDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy(t, ctx, fixture)
}

func TestDependencyEditorRefusesSelfDependencyWithTheProbeSkipped(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "self")
	defer cleanup()
	conformance.RunDependencyEditorRefusesSelfDependencyWithTheProbeSkipped(t, ctx, fixture)
}

func TestDependencyEditorRecordsOneHistoryEntryPerLandedRequest(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "hist")
	defer cleanup()
	conformance.RunDependencyEditorRecordsOneHistoryEntryPerLandedRequest(t, ctx, fixture)
}

func TestDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "ephhist")
	defer cleanup()
	conformance.RunDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest(t, ctx, fixture)
}

func TestDependencyEditorSnapshotsTheRequest(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "snap")
	defer cleanup()
	conformance.RunDependencyEditorSnapshotsTheRequest(t, ctx, fixture)
}

func TestDependencyEditorValidationRefusalsWriteNothing(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "valid")
	defer cleanup()
	conformance.RunDependencyEditorValidationRefusalsWriteNothing(t, ctx, fixture)
}

func TestDependencyEditorRoutesWispSourcedRemovalToTheWispPlane(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "wrm")
	defer cleanup()
	conformance.RunDependencyEditorRoutesWispSourcedRemovalToTheWispPlane(t, ctx, fixture)
}

func TestDependencyEditorRefusesAGhostSource(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "ghostsrc")
	defer cleanup()
	conformance.RunDependencyEditorRefusesAGhostSource(t, ctx, fixture)
}

func TestDependencyEditorRefusesAMissingLocalTarget(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "notgt")
	defer cleanup()
	conformance.RunDependencyEditorRefusesAMissingLocalTarget(t, ctx, fixture)
}

func TestDependencyEditorAcceptsATypeOutsideTheConstants(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "opentype")
	defer cleanup()
	conformance.RunDependencyEditorAcceptsATypeOutsideTheConstants(t, ctx, fixture)
}

func TestDependencyEditorRemovesOnlyTheNamedEdge(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "rmnamed")
	defer cleanup()
	conformance.RunDependencyEditorRemovesOnlyTheNamedEdge(t, ctx, fixture)
}

func TestDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "skipok")
	defer cleanup()
	conformance.RunDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe(t, ctx, fixture)
}

func TestDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest(t *testing.T) {
	fixture, ctx, cleanup := newDoltDependencyEditorFixture(t, "mixhist")
	defer cleanup()
	conformance.RunDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest(t, ctx, fixture)
}

// newDoltDependencyEditorFixture composes the backend's role fixture kit with
// the accessor under test. Every hook but the accessor comes from the kit, so
// the seeding and scalar-query plumbing stays identical to the other roles'.
func newDoltDependencyEditorFixture(t *testing.T, prefix string) (conformance.DependencyEditorFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	editor, err := store.DependencyEditor()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("DependencyEditor(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.DependencyEditorFixture{
		IssuePrefix:  kit.IssuePrefix,
		Editor:       editor,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
