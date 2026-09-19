package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestVersionReconcilerContract runs the VersionReconciler contract against the
// server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. The two version markers are GLOBAL to a
// workspace and cannot be namespaced under the fixture prefix, so every case
// seeds both of them explicitly instead of relying on what the case before it
// left. setupTestStore already marks the PARENT
// parallel; no subtest here calls t.Parallel, because the no-history case takes
// a log delta.
func TestVersionReconcilerContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltVersionReconcilerFixture(t, "vrec")
	defer cleanup()

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

func newDoltVersionReconcilerFixture(t *testing.T, prefix string) (conformance.VersionReconcilerFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	reconciler, err := store.VersionReconciler()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("VersionReconciler(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.VersionReconcilerFixture{
		Reconciler: reconciler,
		// Past the role, through the store's own metadata seam: the role can
		// never leave a marker below the mark, and two cases assert about
		// exactly that state.
		RecordMarkers: func(ctx context.Context, recorded, highWaterMark string) error {
			if err := store.SetLocalMetadata(ctx, workapi.MetadataKeyVersion, recorded); err != nil {
				return err
			}
			return store.SetLocalMetadata(ctx, workapi.MetadataKeyVersionMax, highWaterMark)
		},
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
