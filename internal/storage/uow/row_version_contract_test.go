package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// TestRowVersionContract runs the revision-token matrix against the
// unit-of-work provider — bd serve's composition, and the second vote: its
// writes run domain/db rather than the issueops bodies the two stores share.
//
// One provider for the whole block (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch.
func TestRowVersionContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWRowVersionFixture(t, ctx, "rvc")

	t.Run("LifecycleRowVersionAdvancesPerMutationKind", func(t *testing.T) {
		conformance.RunLifecycleRowVersionAdvancesPerMutationKind(t, ctx, fixture)
	})
	t.Run("LifecycleUpdateLabelOnlyPatchLeavesRowVersion", func(t *testing.T) {
		conformance.RunLifecycleUpdateLabelOnlyPatchLeavesRowVersion(t, ctx, fixture)
	})
}

// newUOWRowVersionFixture takes every subject through the provider's own
// capability accessors, and reads the token back through the provider's
// both-plane issue read, as newUOWLifecycleUpdateFixture does.
func newUOWRowVersionFixture(t *testing.T, ctx context.Context, prefix string) conformance.RowVersionFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	lifecycle, err := provider.(IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	cas, err := provider.(MetadataCASSource).MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS(): %v", err)
	}
	claimer, err := provider.(IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	releaser, err := provider.(ReleaserSource).Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	heartbeater, err := provider.(HeartbeaterSource).Heartbeater()
	if err != nil {
		t.Fatalf("Heartbeater(): %v", err)
	}
	editor, err := provider.(DependencyEditorSource).DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	commenter, err := provider.(CommenterSource).Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	applier, err := provider.(BatchApplierSource).BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
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
		GetIssue: func(ctx context.Context, id string) (*types.Issue, error) {
			return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
				issue, _, err := operationIssue(ctx, uw, id, false)
				return issue, err
			})
		},
		QueryScalar: kit.QueryScalar,
	}
}
