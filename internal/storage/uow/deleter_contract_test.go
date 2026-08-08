package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestDeleterContract runs the Deleter contract against the unit-of-work
// provider — the one implementation that does not run
// internal/storage/issueops.DeleteInTx. It is the SECOND of two votes, not the
// third: the two store backends share the other body.
//
// It is also the backend this role changes most: the proxied route hardcoded
// cascade at both of its call sites and REFUSED `--cascade` as an unsupported
// flag, so there was no way to delete an issue on a team server without taking
// its dependents. The guard and the orphan mode below are new behaviour here.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and issues are database-global.
func TestDeleterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWDeleterFixture(t, ctx, "del")

	t.Run("RefusesAMalformedRequest", func(t *testing.T) {
		conformance.RunDeleterRefusesAMalformedRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnAbsentID", func(t *testing.T) {
		conformance.RunDeleterRefusesAnAbsentID(t, ctx, fixture)
	})
	t.Run("RefusesDependentsOutsideTheRequest", func(t *testing.T) {
		conformance.RunDeleterRefusesDependentsOutsideTheRequest(t, ctx, fixture)
	})
	t.Run("ForceOrphansDependents", func(t *testing.T) {
		conformance.RunDeleterForceOrphansDependents(t, ctx, fixture)
	})
	t.Run("CascadeDeletesTheClosure", func(t *testing.T) {
		conformance.RunDeleterCascadeDeletesTheClosure(t, ctx, fixture)
	})
	t.Run("ErasesAcrossBothPlanes", func(t *testing.T) {
		conformance.RunDeleterErasesAcrossBothPlanes(t, ctx, fixture)
	})
	t.Run("CascadeFromAWispRootDeletesTheClosure", func(t *testing.T) {
		conformance.RunDeleterCascadeFromAWispRootDeletesTheClosure(t, ctx, fixture)
	})
	t.Run("GuardsAWispNamedWithADurableDependent", func(t *testing.T) {
		conformance.RunDeleterGuardsAWispNamedWithADurableDependent(t, ctx, fixture)
	})
	t.Run("GuardsADurableNamedWithAWispDependent", func(t *testing.T) {
		conformance.RunDeleterGuardsADurableNamedWithAWispDependent(t, ctx, fixture)
	})
	t.Run("CountsCrossPlaneEdgesItRemoves", func(t *testing.T) {
		conformance.RunDeleterCountsCrossPlaneEdgesItRemoves(t, ctx, fixture)
	})
	t.Run("NeverCallsALiveRowDeleted", func(t *testing.T) {
		conformance.RunDeleterNeverCallsALiveRowDeleted(t, ctx, fixture)
	})
	t.Run("CollapsesDuplicateIDs", func(t *testing.T) {
		conformance.RunDeleterCollapsesDuplicateIDs(t, ctx, fixture)
	})
	t.Run("RewritesReferencesInNeighbors", func(t *testing.T) {
		conformance.RunDeleterRewritesReferencesInNeighbors(t, ctx, fixture)
	})
	t.Run("DryRunChangesNothing", func(t *testing.T) {
		conformance.RunDeleterDryRunChangesNothing(t, ctx, fixture)
	})
	t.Run("RecordsAtMostOneHistoryEntry", func(t *testing.T) {
		conformance.RunDeleterRecordsAtMostOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunDeleterDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
	t.Run("SettlesTheSurvivorsOfADeletedBlocker", func(t *testing.T) {
		conformance.RunDeleterSettlesTheSurvivorsOfADeletedBlocker(t, ctx, fixture)
	})
	t.Run("SettlesTheChildrenOfADeletedParent", func(t *testing.T) {
		conformance.RunDeleterSettlesTheChildrenOfADeletedParent(t, ctx, fixture)
	})
}

func newUOWDeleterFixture(t *testing.T, ctx context.Context, prefix string) conformance.DeleterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewDeleter: a provider that stopped
	// offering the role is the regression a constructor call would hide.
	source, ok := provider.(DeleterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Deleter accessor", provider)
	}
	deleter, err := source.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.DeleterFixture{
		IssuePrefix:   kit.IssuePrefix,
		Deleter:       deleter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
	}
}
