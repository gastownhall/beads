//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestEmbeddedDependencyEditorRoutesWispSourcedEdgeToTheWispPlane(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRoutesWispSourcedEdgeToTheWispPlane(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "wroute"))
}

func TestEmbeddedDependencyEditorMixedBatchWritesBothPlanes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorMixedBatchWritesBothPlanes(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "wmixed"))
}

func TestEmbeddedDependencyEditorMixedBatchRefusalRollsBackBothPlanes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorMixedBatchRefusalRollsBackBothPlanes(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "wrollback"))
}

func TestEmbeddedDependencyEditorRefusesCrossPlaneCycle(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRefusesCrossPlaneCycle(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "wcycle"))
}

func TestEmbeddedDependencyEditorAddedEchoesTheRequestOrder(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorAddedEchoesTheRequestOrder(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "echo"))
}

func TestEmbeddedDependencyEditorSameTypeReAddIsIdempotent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorSameTypeReAddIsIdempotent(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "idem"))
}

func TestEmbeddedDependencyEditorRepeatsWithinOneRequestCollapse(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRepeatsWithinOneRequestCollapse(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "repeat"))
}

func TestEmbeddedDependencyEditorAttributesItsEventsToTheActor(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorAttributesItsEventsToTheActor(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "depactor"))
}

func TestEmbeddedDependencyEditorRetypeRefusalLeavesTheOriginalEdge(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRetypeRefusalLeavesTheOriginalEdge(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "retype"))
}

func TestEmbeddedDependencyEditorRefusalWritesNothing(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRefusalWritesNothing(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "refuse"))
}

func TestEmbeddedDependencyEditorRemoveIsIdempotent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRemoveIsIdempotent(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "rm"))
}

func TestEmbeddedDependencyEditorAppliesParentChildBeforeBlockingEdges(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorAppliesParentChildBeforeBlockingEdges(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "pcfirst"))
}

func TestEmbeddedDependencyEditorAcceptsAnExternalTarget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorAcceptsAnExternalTarget(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "ext"))
}

func TestEmbeddedDependencyEditorAcceptsAForeignRepoTarget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorAcceptsAForeignRepoTarget(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "foreign"))
}

func TestEmbeddedDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "hier"))
}

func TestEmbeddedDependencyEditorRefusesSelfDependencyWithTheProbeSkipped(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRefusesSelfDependencyWithTheProbeSkipped(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "self"))
}

func TestEmbeddedDependencyEditorRecordsOneHistoryEntryPerLandedRequest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRecordsOneHistoryEntryPerLandedRequest(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "hist"))
}

func TestEmbeddedDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "ephhist"))
}

func TestEmbeddedDependencyEditorSnapshotsTheRequest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorSnapshotsTheRequest(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "snap"))
}

func TestEmbeddedDependencyEditorValidationRefusalsWriteNothing(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorValidationRefusalsWriteNothing(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "valid"))
}

func TestEmbeddedDependencyEditorRoutesWispSourcedRemovalToTheWispPlane(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRoutesWispSourcedRemovalToTheWispPlane(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "wrm"))
}

func TestEmbeddedDependencyEditorRefusesAGhostSource(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRefusesAGhostSource(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "ghostsrc"))
}

func TestEmbeddedDependencyEditorRefusesAMissingLocalTarget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRefusesAMissingLocalTarget(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "notgt"))
}

func TestEmbeddedDependencyEditorAcceptsATypeOutsideTheConstants(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorAcceptsATypeOutsideTheConstants(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "opentype"))
}

func TestEmbeddedDependencyEditorRemovesOnlyTheNamedEdge(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRemovesOnlyTheNamedEdge(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "rmnamed"))
}

func TestEmbeddedDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "skipok"))
}

func TestEmbeddedDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest(t, ctx, newEmbeddedDependencyEditorFixture(t, ctx, "mixhist"))
}

// newEmbeddedDependencyEditorFixture composes the backend's role fixture kit
// with the accessor under test. Every hook but the accessor comes from the kit,
// so the seeding and scalar-query plumbing stays identical to the other roles'.
func newEmbeddedDependencyEditorFixture(t *testing.T, ctx context.Context, prefix string) conformance.DependencyEditorFixture {
	t.Helper()
	te := newTestEnv(t, prefix)
	editor, err := te.store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.DependencyEditorFixture{
		IssuePrefix:  kit.IssuePrefix,
		Editor:       editor,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
