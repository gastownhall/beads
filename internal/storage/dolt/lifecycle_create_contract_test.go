package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLifecycleCreateContract runs the accessor-only Create half of the
// Lifecycle contract against the server-backed store. It shares its create body
// with the embedded store (internal/storage/issueops), so this wiring and the
// embedded one are ONE vote on the semantics; the unit-of-work wiring is the
// second.
//
// The cases are subtests of one parent so the whole block shares one store and
// one copy-on-write branch: each Run namespaces its ids under the fixture
// prefix. setupTestStore already marks the PARENT parallel; no subtest here
// calls t.Parallel.
func TestLifecycleCreateContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltLifecycleCreateFixture(t, "lcc")
	defer cleanup()

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

func newDoltLifecycleCreateFixture(t *testing.T, prefix string) (conformance.LifecycleCreateFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the capability accessor, not NewIssueOperations: a store that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("set issue_prefix to %q: %v", prefix, err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.LifecycleCreateFixture{
		IssuePrefix: kit.IssuePrefix,
		Lifecycle:   lifecycle,
		CreateIssue: kit.CreateIssue,
		// The frozen kit reads through QueryScalar. This block reads its
		// post-state through the store's own reads instead, so no case in it
		// depends on raw SQL.
		GetIssue:   store.GetIssue,
		WispExists: newDoltContractWispProbe(store),
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
