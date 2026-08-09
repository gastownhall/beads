//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLifecycleUpdateContract runs the accessor-only Update half of the
// Lifecycle contract against the embedded store, which shares its
// validate/execute body with the server-backed store and differs in the
// transaction wrapper and the engine underneath. That is what this wiring
// catches; it is not an independent vote on the body.
//
// One environment for the whole block: booting an embedded engine per case
// would dominate the runtime, and the ids are prefix-namespaced.
func TestLifecycleUpdateContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "lup")
	ctx := t.Context()
	fixture := newEmbeddedLifecycleUpdateFixture(t, te, "lup")

	t.Run("PersistsThePatchAndHydratesTheResult", func(t *testing.T) {
		conformance.RunLifecycleUpdatePersistsThePatchAndHydratesTheResult(t, ctx, fixture)
	})
	t.Run("ReportsNoChangeForASameValuePatch", func(t *testing.T) {
		conformance.RunLifecycleUpdateReportsNoChangeForASameValuePatch(t, ctx, fixture)
	})
	t.Run("AppendsNotesWithoutReplacingThem", func(t *testing.T) {
		conformance.RunLifecycleUpdateAppendsNotesWithoutReplacingThem(t, ctx, fixture)
	})
	t.Run("ClearsTheNullableMembers", func(t *testing.T) {
		conformance.RunLifecycleUpdateClearsTheNullableMembers(t, ctx, fixture)
	})
	t.Run("ReplacesTheLabelSet", func(t *testing.T) {
		conformance.RunLifecycleUpdateReplacesTheLabelSet(t, ctx, fixture)
	})
	t.Run("ResolvesBothPlanesUnlessRestricted", func(t *testing.T) {
		conformance.RunLifecycleUpdateResolvesBothPlanesUnlessRestricted(t, ctx, fixture)
	})
	t.Run("RefusesUnknownIDsAndActorlessRequests", func(t *testing.T) {
		conformance.RunLifecycleUpdateRefusesUnknownIDsAndActorlessRequests(t, ctx, fixture)
	})
	t.Run("RefusalWritesNoMemberOfThePatch", func(t *testing.T) {
		conformance.RunLifecycleUpdateRefusalWritesNoMemberOfThePatch(t, ctx, fixture)
	})
}

func newEmbeddedLifecycleUpdateFixture(t *testing.T, te *testEnv, prefix string) conformance.LifecycleUpdateFixture {
	t.Helper()
	// Through the capability accessor, not NewIssueOperations: a store that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.LifecycleUpdateFixture{
		IssuePrefix:   kit.IssuePrefix,
		Lifecycle:     lifecycle,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// The frozen kit reads through QueryScalar. This block reads its
		// post-state through the store's own issue read instead, so no case in
		// it depends on raw SQL.
		GetIssue: te.store.GetIssue,
	}
}
