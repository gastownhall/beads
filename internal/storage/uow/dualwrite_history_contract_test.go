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

func newUOWDualWriteFixture(t *testing.T, ctx context.Context, prefix string, enabled bool) conformance.DualWriteFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor's operator half, the same way
	// newUOWJournalFixture reaches storage.EventsJournalConfigurer: a
	// provider that stopped offering the role is the regression this
	// assertion exists to catch.
	configurer, ok := provider.(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("provider %T does not implement storage.VersionedHistoryConfigurer", provider)
	}
	configurer.SetVersionedHistoryEnabled(enabled)

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
