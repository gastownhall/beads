package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestParticipationGenerationFence runs the design §16.2(b) write-fence
// contract (backend/conformance/dualwrite_participation_generation_contract.go)
// against the unit-of-work provider. Like TestDualWriteContract, this is an
// engine check rather than a second vote: all three legs share
// RecordVersionInTx's one body, and this is the only leg where the mutation's
// own commit is assembled by composition rather than opened directly against
// a *sql.DB.
func TestParticipationGenerationFence(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWParticipationGenerationFixture(t, ctx, "pg")

	t.Run("CreateWithFlagOnStampsAValue", func(t *testing.T) {
		conformance.RunParticipationGenerationCreateWithFlagOnStampsAValue(t, ctx, fixture)
	})
	t.Run("CreateWithFlagOffLeavesLegacyNull", func(t *testing.T) {
		conformance.RunParticipationGenerationCreateWithFlagOffLeavesLegacyNull(t, ctx, fixture)
	})
	t.Run("SkipsUpdateOnLegacyRecord", func(t *testing.T) {
		conformance.RunParticipationGenerationSkipsUpdateOnLegacyRecord(t, ctx, fixture)
	})
	t.Run("ProceedsForPromotedRecord", func(t *testing.T) {
		conformance.RunParticipationGenerationProceedsForPromotedRecord(t, ctx, fixture)
	})
	t.Run("FenceIgnoresSchemaSkewHatch", func(t *testing.T) {
		conformance.RunParticipationGenerationFenceIgnoresSchemaSkewHatch(t, ctx, fixture)
	})
	t.Run("WispPromotionBeginsHistory", func(t *testing.T) {
		conformance.RunParticipationGenerationWispPromotionBeginsHistory(t, ctx, fixture)
	})
	t.Run("WispPersistenceMoveBeginsHistory", func(t *testing.T) {
		conformance.RunParticipationGenerationWispPersistenceMoveBeginsHistory(t, ctx, fixture)
	})
}

// TestParticipationGenerationFixtureKitIsWired is this leg's half of the
// explicit per-leg guardrail design §8.5 calls for in place of AST
// auto-discovery — see the dolt leg's TestParticipationGenerationFixtureKitIsWired
// for why the guardrail is needed even though TestEveryLegWiresEveryRoleContract's
// scan already enumerates and confirms wiring for every
// RunParticipationGenerationXxx entrypoint.
func TestParticipationGenerationFixtureKitIsWired(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWParticipationGenerationFixture(t, ctx, "pgk")

	if fixture.SetFlag == nil {
		t.Error("ParticipationGenerationFixture.SetFlag is nil")
	}
	if fixture.Mutate == nil {
		t.Error("ParticipationGenerationFixture.Mutate is nil")
	}
	if fixture.MutateExisting == nil {
		t.Error("ParticipationGenerationFixture.MutateExisting is nil")
	}
	if fixture.CreateWisp == nil {
		t.Error("ParticipationGenerationFixture.CreateWisp is nil")
	}
	if fixture.PromoteWisp == nil {
		t.Error("ParticipationGenerationFixture.PromoteWisp is nil")
	}
	if fixture.MovePersistent == nil {
		t.Error("ParticipationGenerationFixture.MovePersistent is nil")
	}
	if fixture.CurrentRevision == nil {
		t.Error("ParticipationGenerationFixture.CurrentRevision is nil")
	}
	if fixture.VersionRowCount == nil {
		t.Error("ParticipationGenerationFixture.VersionRowCount is nil")
	}
	if fixture.ParticipationGeneration == nil {
		t.Error("ParticipationGenerationFixture.ParticipationGeneration is nil")
	}
}

func newUOWParticipationGenerationFixture(t *testing.T, ctx context.Context, prefix string) conformance.ParticipationGenerationFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor's operator half, the same way
	// newUOWVersionedHistoryProvider reaches it above — SetFlag here needs to
	// toggle more than once per case, so this fixture keeps its own handle
	// rather than fixing the flag once at construction the way
	// newUOWVersionedHistoryProvider does for the dual-write fixture.
	configurer, ok := provider.(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("provider %T does not implement storage.VersionedHistoryConfigurer", provider)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.ParticipationGenerationFixture{
		IssuePrefix: prefix,
		SetFlag: func(ctx context.Context, enabled bool) error {
			configurer.SetVersionedHistoryEnabled(enabled)
			return nil
		},
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
		MutateExisting: func(ctx context.Context, id string) error {
			// A different value than Mutate gave it, not a re-assertion of the
			// same title: DiscardNoopIssueUpdates would discard the latter
			// before it ever reached the fence this contract is testing.
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "update " + id, uw.IssueUseCase().UpdateIssue(ctx, id,
					map[string]any{"title": "updated-" + id}, "actor")
			})
		},
		CreateWisp: func(ctx context.Context, id string) error {
			return kit.CreateWisp(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
			}, "actor")
		},
		PromoteWisp: func(ctx context.Context, id string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "promote " + id, uw.IssueUseCase().PromoteWisp(ctx, id, "actor")
			})
		},
		MovePersistent: func(ctx context.Context, id string) error {
			// ApplyUpdate's persistence step, which runs MovePersistence and
			// so MoveIssuePersistenceInTx's own mint, not a guarded update's.
			persistent := types.PersistenceModePersistent
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				_, err := uw.IssueUseCase().ApplyUpdate(ctx, id, domain.UpdateSpec{Persistence: &persistent}, "actor")
				return "move persistent " + id, err
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
		ParticipationGeneration: func(ctx context.Context, id string) (*int64, error) {
			// scanRawSQLValue refuses a NULL numeric destination outright
			// (issue_operations_contract_test.go's frozen scaffolding surface,
			// bd-kue5t) rather than decaying it to zero, so a bare
			// participation_generation read cannot scan into *sql.NullInt64 the
			// way the dolt/embeddeddolt legs' std-library Scan does. epoch is
			// seeded at 1 and only ever increments (version_history.go), so a
			// stamped participation_generation is always >= 1 and -1 is an
			// unambiguous "still NULL" sentinel for the COALESCE.
			var gen int64
			if err := kit.QueryScalar(ctx,
				"SELECT COALESCE(participation_generation, -1) FROM issues WHERE id = ?", []any{id}, &gen); err != nil {
				return nil, err
			}
			if gen == -1 {
				return nil, nil
			}
			return &gen, nil
		},
	}
}
