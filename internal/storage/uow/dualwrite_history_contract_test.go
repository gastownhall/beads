package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestDualWriteContract runs the dual-write history contract against the
// unit-of-work provider. All three legs share RecordVersionInTx's one body,
// so this is an engine check rather than a second vote — and here it checks
// the composition, the part that genuinely differs on this leg: dual-write
// history has to land inside the SAME unit of work as the mutation it
// accompanies, and this is the only leg where that mutation's own commit is
// assembled by composition rather than opened directly against a *sql.DB.
func TestDualWriteContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWDualWriteFixture(t, ctx, "dwc", true)

	t.Run("MintsOneVersionRowPerAcceptedMutation", func(t *testing.T) {
		conformance.RunDualWriteMintsOneVersionRowPerAcceptedMutation(t, ctx, fixture)
	})
	t.Run("NoOpMutationMintsNoRow", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationMintsNoRow(t, ctx, fixture)
	})
	t.Run("AttributionIsRecordedWithTheMutation", func(t *testing.T) {
		conformance.RunDualWriteAttributionIsRecordedWithTheMutation(t, ctx, fixture)
	})
	t.Run("CurrentRevisionMatchesTheNewVersionRow", func(t *testing.T) {
		conformance.RunDualWriteCurrentRevisionMatchesTheNewVersionRow(t, ctx, fixture)
	})
	t.Run("NoOpMutationLeavesThePriorVersionRowUnperturbed", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationLeavesThePriorVersionRowUnperturbed(t, ctx, fixture)
	})
}

// TestDualWriteContractFlagOff runs FR-7 against a provider constructed with
// the flag OFF from the start — its own provider, not a shared one, so "flag
// off" means what FR-7 promises rather than "not yet turned on this session".
func TestDualWriteContractFlagOff(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWDualWriteFixture(t, ctx, "dwcoff", false)

	t.Run("FlagOffProducesNoVersionRows", func(t *testing.T) {
		conformance.RunDualWriteFlagOffProducesNoVersionRows(t, ctx, fixture)
	})
}

// TestDualWriteFixtureKitIsWired is this leg's half of the explicit
// per-leg guardrail design §8.5 calls for in place of AST auto-discovery —
// see the dolt leg's TestDualWriteFixtureKitIsWired for why the guardrail is
// needed even though TestEveryLegWiresEveryRoleContract's scan already
// enumerates and confirms wiring for all six RunDualWriteXxx entrypoints.
func TestDualWriteFixtureKitIsWired(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWDualWriteFixture(t, ctx, "dwk", true)

	if fixture.Mutate == nil {
		t.Error("DualWriteFixture.Mutate is nil")
	}
	if fixture.MutateAsNoOp == nil {
		t.Error("DualWriteFixture.MutateAsNoOp is nil")
	}
	if fixture.CurrentRevision == nil {
		t.Error("DualWriteFixture.CurrentRevision is nil")
	}
	if fixture.VersionRowCount == nil {
		t.Error("DualWriteFixture.VersionRowCount is nil")
	}
	if fixture.LatestVersionAttribution == nil {
		t.Error("DualWriteFixture.LatestVersionAttribution is nil")
	}
}

// TestUOWCreateWithLabelAndEdgeMintsPerRepositoryWrite_KnownDivergence makes
// the uow leg's 1+N+M divergence machine-visible (reviewer MINOR-3 on
// gastownhall/beads#6358). TestDualWriteContract's Mutate creates an issue
// with no labels and no dependencies, so its "exactly one version row per
// accepted mutation" case cannot see what a fuller create does on this leg:
// domain.IssueUseCase.CreateIssue inserts the row and then persists each
// label and each edge through its own repository (domain/db label.go and
// dependency.go), and every one of those repository writes reaches
// issueops.RecordVersionInTx on its own — 1 + N + M rows for ONE
// caller-visible create. The direct legs (dolt, embeddeddolt) mint exactly
// one row for the same shape: issueops.CreateIssuesInTxWithContext runs its
// constituents without minting and mints once per issue after the
// creation-time edges land.
//
// This test pins the CURRENT, measured behavior — three rows for one label
// and one edge — so the divergence is a red test away from being fixed
// rather than a comment. KNOWN DIVERGENCE gastownhall/beads#6379:
// it is EXPECTED TO FLIP when that follow-up lands and the uow create mints
// once; update the want to 1 then and fold the shape into the contract.
func TestUOWCreateWithLabelAndEdgeMintsPerRepositoryWrite_KnownDivergence(t *testing.T) {
	ctx := context.Background()
	const prefix = "dwle"
	provider := newUOWVersionedHistoryProvider(t, ctx, prefix, true)
	kit := newUOWRoleFixtureKit(provider, prefix)

	// The edge needs a target that exists; seeded through the kit, with no
	// labels and no edges, it doubles as the control for the bare shape.
	target := prefix + "-target"
	if err := kit.CreateIssue(ctx, &types.Issue{
		ID: target, Title: "t-" + target, IssueType: types.TypeTask, Status: types.StatusOpen,
	}, "actor"); err != nil {
		t.Fatalf("seeding %s: %v", target, err)
	}

	id := prefix + "-label-and-edge"
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue: &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
			},
			ExplicitID:   id,
			Labels:       []string{"one"},
			Dependencies: []domain.DependencySpec{{Type: types.DepBlocks, TargetID: target}},
			CreateOnly:   true,
		}, "actor")
		return "create " + id, err
	}); err != nil {
		t.Fatalf("creating %s with one label and one edge: %v", id, err)
	}

	count := func(what, query string, args ...any) int {
		t.Helper()
		var n int
		if err := kit.QueryScalar(ctx, query, args, &n); err != nil {
			t.Fatalf("counting %s: %v", what, err)
		}
		return n
	}
	// The rows counted below belong to a create that really carried both
	// writes, not to one that dropped a label or an edge on the way in.
	if labels := count("labels", "SELECT COUNT(*) FROM labels WHERE issue_id = ?", id); labels != 1 {
		t.Fatalf("labels on %s = %d, want 1", id, labels)
	}
	if edges := count("edges", "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND "+
		"COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?", id, target); edges != 1 {
		t.Fatalf("edges %s -> %s = %d, want 1", id, target, edges)
	}

	// 1 issue row + 1 label + 1 edge, each minted by its own repository write.
	// The direct legs mint exactly 1 for this shape.
	const mintedToday = 3
	got := count("version rows for "+id, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", id)
	if got != mintedToday {
		t.Errorf("version rows for %s after one create with 1 label + 1 edge = %d, want the %d this leg mints today "+
			"(1 issue + 1 label + 1 edge); 1 would mean gastownhall/beads#6379 landed — flip this test into the contract",
			id, got, mintedToday)
	}
	// The bare shape on the same leg mints one row, so the two extra rows
	// above are the label and the edge, not something else about this leg.
	if bare := count("version rows for "+target, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", target); bare != 1 {
		t.Errorf("version rows for %s (no labels, no edges) = %d, want 1", target, bare)
	}
	t.Logf("KNOWN DIVERGENCE gastownhall/beads#6379: uow create with 1 label + 1 edge minted %d version rows "+
		"(1 issue + 1 label + 1 edge, one per repository write); the direct legs mint exactly 1 for the same shape. "+
		"This test is expected to FLIP when the follow-up lands.", got)
}

// newUOWVersionedHistoryProvider boots one provider for a versioned-history
// suite and sets the flag as asked — through the capability accessor's
// operator half, the same way newUOWJournalFixture reaches
// storage.EventsJournalConfigurer: a provider that stopped offering the role
// is the regression this assertion exists to catch.
func newUOWVersionedHistoryProvider(t *testing.T, ctx context.Context, prefix string, enabled bool) UnitOfWorkProvider {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	configurer, ok := provider.(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("provider %T does not implement storage.VersionedHistoryConfigurer", provider)
	}
	configurer.SetVersionedHistoryEnabled(enabled)
	return provider
}

func newUOWDualWriteFixture(t *testing.T, ctx context.Context, prefix string, enabled bool) conformance.DualWriteFixture {
	t.Helper()
	provider := newUOWVersionedHistoryProvider(t, ctx, prefix, enabled)
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.DualWriteFixture{
		IssuePrefix: prefix,
		Mutate: func(ctx context.Context, id string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
					Issue: &types.Issue{
						ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
					},
					ExplicitID: id,
					CreateOnly: true,
				}, "actor")
				return "create " + id, err
			})
		},
		MutateAsNoOp: func(ctx context.Context, id string) error {
			// Re-sets title to the exact value Mutate already gave it, so
			// issueops.DiscardNoopIssueUpdates discards it before it ever
			// reaches RecordVersionInTx.
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "update " + id, uw.IssueUseCase().UpdateIssue(ctx, id,
					map[string]any{"title": "t-" + id}, "actor")
			})
		},
		CurrentRevision: func(ctx context.Context, id string) (int64, error) {
			var revision int64
			err := kit.QueryScalar(ctx, "SELECT current_revision FROM issues WHERE id = ?", []any{id}, &revision)
			return revision, err
		},
		VersionRowCount: func(ctx context.Context, id string) (int, error) {
			var count int
			err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{id}, &count)
			return count, err
		},
		LatestVersionAttribution: func(ctx context.Context, id string) (actor, agent, message string, err error) {
			err = kit.QueryScalar(ctx, `
				SELECT COALESCE(change_actor, ''), COALESCE(change_agent, ''), COALESCE(change_message, '')
				FROM issue_versions
				WHERE issue_id = ?
				ORDER BY revision DESC
				LIMIT 1`, []any{id}, &actor, &agent, &message)
			return actor, agent, message, err
		},
	}
}
