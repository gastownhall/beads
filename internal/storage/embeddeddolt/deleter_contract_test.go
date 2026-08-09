//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestDeleterContract runs the Deleter contract against the embedded store,
// which hands back the SAME body the server-backed store does
// (internal/storage/issueops.DeleteInTx) and differs only in how it reaches a
// transaction and in that its commit runs outside one. That is what this wiring
// catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, every case namespaces its seeds, and the history
// delta needs the subtests sequential anyway.
func TestDeleterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "del")
	ctx := t.Context()
	fixture := newEmbeddedDeleterFixture(t, te, "del")

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
	t.Run("SettlesTheSurvivorsOfADeletedWispBlocker", func(t *testing.T) {
		conformance.RunDeleterSettlesTheSurvivorsOfADeletedWispBlocker(t, ctx, fixture)
	})
	t.Run("SettlesTheChildrenOfADeletedParent", func(t *testing.T) {
		conformance.RunDeleterSettlesTheChildrenOfADeletedParent(t, ctx, fixture)
	})
}

func newEmbeddedDeleterFixture(t *testing.T, te *testEnv, prefix string) conformance.DeleterFixture {
	t.Helper()
	deleter, err := te.store.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
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
