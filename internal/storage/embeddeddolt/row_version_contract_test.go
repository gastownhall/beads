//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestRowVersionContract runs the revision-token matrix against the embedded
// store. It shares the issueops write bodies with the server-backed store, so
// this wiring and the dolt one are one vote on the semantics and two on the
// transaction wrappers; the unit-of-work wiring is the second vote.
//
// One environment for the block: the ids are prefix-namespaced per row.
func TestRowVersionContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rvc")
	ctx := t.Context()
	fixture := newEmbeddedRowVersionFixture(t, te, "rvc")

	t.Run("LifecycleRowVersionAdvancesPerMutationKind", func(t *testing.T) {
		conformance.RunLifecycleRowVersionAdvancesPerMutationKind(t, ctx, fixture)
	})
	t.Run("LifecycleUpdateLabelOnlyPatchLeavesRowVersion", func(t *testing.T) {
		conformance.RunLifecycleUpdateLabelOnlyPatchLeavesRowVersion(t, ctx, fixture)
	})
}

func newEmbeddedRowVersionFixture(t *testing.T, te *testEnv, prefix string) conformance.RowVersionFixture {
	t.Helper()
	store := te.store
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	// Every subject through its capability accessor, not a constructor: a
	// store that stopped offering a role is the regression.
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	cas, err := store.MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS(): %v", err)
	}
	claimer, err := store.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	releaser, err := store.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	heartbeater, err := store.Heartbeater()
	if err != nil {
		t.Fatalf("Heartbeater(): %v", err)
	}
	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	commenter, err := store.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	applier, err := store.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
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
	}
}
