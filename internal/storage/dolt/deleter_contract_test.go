package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestDeleterContract runs the Deleter contract against the server-backed
// store, which reaches internal/storage/issueops.DeleteInTx through its own
// write transaction and is the ONE wiring that records a version-control entry
// for a deletion.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch, which is why every case namespaces its seeds
// under fixture.IssuePrefix plus its own tag. setupTestStore already marks the
// PARENT parallel; no subtest here calls t.Parallel, because
// RecordsAtMostOneHistoryEntry takes a before/after delta.
func TestDeleterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltDeleterFixture(t, "del")
	defer cleanup()

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

// newDoltDeleterFixture composes the frozen role kit with this backend's
// accessor.
func newDoltDeleterFixture(t *testing.T, prefix string) (conformance.DeleterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	deleter, err := store.Deleter()
	if err != nil {
		stop()
		t.Fatalf("Deleter(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.DeleterFixture{
		IssuePrefix:   kit.IssuePrefix,
		Deleter:       deleter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
	}, ctx, stop
}
