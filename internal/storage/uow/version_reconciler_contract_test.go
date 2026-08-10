package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestVersionReconcilerContract runs the VersionReconciler contract against the
// unit-of-work provider — the one implementation that does not hand back
// internal/workapi/storeversionreconciler, so this is the wiring where a
// genuine body divergence shows up. The two store backends share that body
// between them, which makes this the SECOND of two votes rather than the third.
//
// It is also the only wiring where "the marker is still there afterwards" is a
// question with a hard answer: this body reads, plans and writes inside ONE
// transaction and every assertion reads back through a NEW one, so a write that
// never committed shows up here and nowhere else.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so the metadata plane and dolt_log are database-global.
func TestVersionReconcilerContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWVersionReconcilerFixture(t, ctx, "vrec")

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

func newUOWVersionReconcilerFixture(t *testing.T, ctx context.Context, prefix string) conformance.VersionReconcilerFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewVersionReconciler: a provider
	// that stopped offering the role is the regression, and a constructor call
	// would hide it.
	source, ok := provider.(VersionReconcilerSource)
	if !ok {
		t.Fatalf("provider %T does not offer the VersionReconciler accessor", provider)
	}
	reconciler, err := source.VersionReconciler()
	if err != nil {
		t.Fatalf("VersionReconciler(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.VersionReconcilerFixture{
		Reconciler: reconciler,
		// Past the role, through the metadata seam its body writes through, in
		// its own committed transaction: the role can never leave a marker
		// below the mark, and two cases assert about exactly that state.
		RecordMarkers: func(ctx context.Context, recorded, highWaterMark string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				if err := uw.ConfigUseCase().SetLocalMetadata(ctx, workapi.MetadataKeyVersion, recorded); err != nil {
					return "", err
				}
				return "bd: seed version markers",
					uw.ConfigUseCase().SetLocalMetadata(ctx, workapi.MetadataKeyVersionMax, highWaterMark)
			})
		},
		CountHistory: kit.CountHistory,
	}
}
