//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLifecycleCreateContract runs the accessor-only Create half of the
// Lifecycle contract against the embedded store, which shares its create body
// with the server-backed store and differs in the transaction wrapper and the
// engine underneath. That is what this wiring catches; it is not an independent
// vote on the body.
//
// One environment for the whole block: booting an embedded engine per case would
// dominate the runtime, and the ids are prefix-namespaced.
func TestLifecycleCreateContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "lcc")
	ctx := t.Context()
	fixture := newEmbeddedLifecycleCreateFixture(t, te, "lcc")

	t.Run("RejectsMissingDependencyTargets", func(t *testing.T) {
		conformance.RunLifecycleCreateRejectsMissingDependencyTargets(t, ctx, fixture)
	})
	t.Run("RefusesAnOccupiedID", func(t *testing.T) {
		conformance.RunLifecycleCreateRefusesAnOccupiedID(t, ctx, fixture)
	})
	t.Run("RefusesAForeignIDPrefix", func(t *testing.T) {
		conformance.RunLifecycleCreateRefusesAForeignIDPrefix(t, ctx, fixture)
	})
	t.Run("InheritsParentLabels", func(t *testing.T) {
		conformance.RunLifecycleCreateInheritsParentLabels(t, ctx, fixture)
	})
	t.Run("WritesEveryScalarField", func(t *testing.T) {
		conformance.RunLifecycleCreateWritesEveryScalarField(t, ctx, fixture)
	})
}

func newEmbeddedLifecycleCreateFixture(t *testing.T, te *testEnv, prefix string) conformance.LifecycleCreateFixture {
	t.Helper()
	// Through the capability accessor, not NewIssueOperations: a store that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.LifecycleCreateFixture{
		IssuePrefix: kit.IssuePrefix,
		Lifecycle:   lifecycle,
		CreateIssue: kit.CreateIssue,
		// The frozen kit reads through QueryScalar. This block reads its
		// post-state through the store's own reads instead, so no case in it
		// depends on raw SQL.
		GetIssue:   te.store.GetIssue,
		WispExists: newEmbeddedContractWispProbe(te),
	}
}
