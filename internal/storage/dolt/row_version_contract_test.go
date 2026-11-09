package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestRowVersionContract runs the revision-token matrix against the
// server-backed store. It shares the issueops write bodies with the embedded
// store, so the two wirings are one vote on the semantics; the unit-of-work
// wiring is the second.
//
// The cases are subtests of one parent so the block shares one store and one
// copy-on-write branch; each row namespaces its ids. setupTestStore already
// marks the PARENT parallel; no subtest here calls t.Parallel.
func TestRowVersionContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltRowVersionFixture(t, "rvc")
	defer cleanup()

	t.Run("LifecycleRowVersionAdvancesPerMutationKind", func(t *testing.T) {
		conformance.RunLifecycleRowVersionAdvancesPerMutationKind(t, ctx, fixture)
	})
	t.Run("LifecycleUpdateLabelOnlyPatchLeavesRowVersion", func(t *testing.T) {
		conformance.RunLifecycleUpdateLabelOnlyPatchLeavesRowVersion(t, ctx, fixture)
	})
}

func newDoltRowVersionFixture(t *testing.T, prefix string) (conformance.RowVersionFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	fail := func(what string, err error) {
		stop()
		t.Fatalf("%s: %v", what, err)
	}
	// Every subject through its capability accessor, not a constructor: a
	// store that stopped offering a role is the regression.
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		fail("IssueLifecycle()", err)
	}
	cas, err := store.MetadataCAS()
	if err != nil {
		fail("MetadataCAS()", err)
	}
	claimer, err := store.IssueClaimer()
	if err != nil {
		fail("IssueClaimer()", err)
	}
	releaser, err := store.Releaser()
	if err != nil {
		fail("Releaser()", err)
	}
	heartbeater, err := store.Heartbeater()
	if err != nil {
		fail("Heartbeater()", err)
	}
	editor, err := store.DependencyEditor()
	if err != nil {
		fail("DependencyEditor()", err)
	}
	commenter, err := store.Commenter()
	if err != nil {
		fail("Commenter()", err)
	}
	applier, err := store.BatchApplier()
	if err != nil {
		fail("BatchApplier()", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.RowVersionFixture{
		IssuePrefix:      kit.IssuePrefix,
		Lifecycle:        lifecycle,
		MetadataCAS:      cas,
		Claimer:          claimer,
		Releaser:         releaser,
		Heartbeater:      heartbeater,
		DependencyEditor: editor,
		Commenter:        commenter,
		BatchApplier:     applier,
		CreateIssue:      kit.CreateIssue,
		AddDependency:    kit.AddDependency,
		SeedClaimer:      claimer,
		GetIssue:         store.GetIssue,
		QueryScalar:      kit.QueryScalar,
	}, ctx, stop
}
