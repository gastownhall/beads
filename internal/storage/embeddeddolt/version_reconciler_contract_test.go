//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestVersionReconcilerContract runs the VersionReconciler contract against the
// embedded store, which hands back the SAME body the server-backed store does
// (internal/workapi/storeversionreconciler) and writes through the same
// SetLocalMetadata, differing only in the engine underneath. That is what this
// wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite, and that is a correctness requirement:
// the two version markers are global to a workspace, so the subtests have to
// run sequentially over one plane, each seeding the state it asserts about.
func TestVersionReconcilerContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "vrec")
	ctx := t.Context()
	fixture := newEmbeddedVersionReconcilerFixture(t, te, "vrec")

	t.Run("RecordsAWorkspaceWithNoMarkers", func(t *testing.T) {
		conformance.RunVersionReconcilerRecordsAWorkspaceWithNoMarkers(t, ctx, fixture)
	})
	t.Run("AdvancesBothMarkersOnAnUpgrade", func(t *testing.T) {
		conformance.RunVersionReconcilerAdvancesBothMarkersOnAnUpgrade(t, ctx, fixture)
	})
	t.Run("TreatsTheSameVersionAsANoOp", func(t *testing.T) {
		conformance.RunVersionReconcilerTreatsTheSameVersionAsANoOp(t, ctx, fixture)
	})
	t.Run("RefusesADowngradeWithoutAnError", func(t *testing.T) {
		conformance.RunVersionReconcilerRefusesADowngradeWithoutAnError(t, ctx, fixture)
	})
	t.Run("RefusesAVersionBelowTheHighWaterMark", func(t *testing.T) {
		conformance.RunVersionReconcilerRefusesAVersionBelowTheHighWaterMark(t, ctx, fixture)
	})
	t.Run("CatchesUpToTheHighWaterMark", func(t *testing.T) {
		conformance.RunVersionReconcilerCatchesUpToTheHighWaterMark(t, ctx, fixture)
	})
	t.Run("RefusesAnEmptyVersion", func(t *testing.T) {
		conformance.RunVersionReconcilerRefusesAnEmptyVersion(t, ctx, fixture)
	})
	t.Run("LeavesTheMarkersStandingWhenItCannotComplete", func(t *testing.T) {
		conformance.RunVersionReconcilerLeavesTheMarkersStandingWhenItCannotComplete(t, ctx, fixture)
	})
	t.Run("RecordsNoHistory", func(t *testing.T) {
		conformance.RunVersionReconcilerRecordsNoHistory(t, ctx, fixture)
	})
}

func newEmbeddedVersionReconcilerFixture(t *testing.T, te *testEnv, prefix string) conformance.VersionReconcilerFixture {
	t.Helper()
	reconciler, err := te.store.VersionReconciler()
	if err != nil {
		t.Fatalf("VersionReconciler(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.VersionReconcilerFixture{
		Reconciler: reconciler,
		// Past the role, through the store's own metadata seam: the role can
		// never leave a marker below the mark, and two cases assert about
		// exactly that state.
		RecordMarkers: func(ctx context.Context, recorded, highWaterMark string) error {
			if err := te.store.SetLocalMetadata(ctx, workapi.MetadataKeyVersion, recorded); err != nil {
				return err
			}
			return te.store.SetLocalMetadata(ctx, workapi.MetadataKeyVersionMax, highWaterMark)
		},
		CountHistory: kit.CountHistory,
	}
}
