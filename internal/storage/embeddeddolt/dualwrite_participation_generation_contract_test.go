//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestParticipationGenerationFence runs the design §16.2(b) write-fence
// contract (backend/conformance/dualwrite_participation_generation_contract.go)
// against the embedded store. Like TestDualWriteContract, this is an ENGINE
// CHECK rather than an independent vote: all three legs share
// RecordVersionInTx's one fence check, and this leg is where a wrapper that
// loses the transaction the fence's skip-or-proceed decision must land in
// would have somewhere concrete to go wrong.
func TestParticipationGenerationFence(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "pg")
	ctx := t.Context()
	fixture := newEmbeddedParticipationGenerationFixture(t, te, "pg")

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
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "pgk")
	fixture := newEmbeddedParticipationGenerationFixture(t, te, "pgk")

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

func newEmbeddedParticipationGenerationFixture(t *testing.T, te *testEnv, prefix string) conformance.ParticipationGenerationFixture {
	t.Helper()
	store := te.store
	// Through the type assertion `bd serve` makes, never the concrete method
	// set — same discipline as newEmbeddedDualWriteFixture above.
	configurer, ok := any(store).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", store)
	}

	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.ParticipationGenerationFixture{
		IssuePrefix: prefix,
		SetFlag: func(ctx context.Context, enabled bool) error {
			configurer.SetVersionedHistoryEnabled(enabled)
			return nil
		},
		Mutate: func(ctx context.Context, id string) error {
			return store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
			}, "actor")
		},
		MutateExisting: func(ctx context.Context, id string) error {
			// A different value than Mutate gave it, not a re-assertion of the
			// same title: DiscardNoopIssueUpdates would discard the latter
			// before it ever reached the fence this contract is testing.
			return store.UpdateIssue(ctx, id, map[string]any{"title": "updated-" + id}, "actor")
		},
		CreateWisp: func(ctx context.Context, id string) error {
			return store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
			}, "actor")
		},
		PromoteWisp: func(ctx context.Context, id string) error {
			return store.PromoteFromEphemeral(ctx, id, "actor")
		},
		MovePersistent: func(ctx context.Context, id string) error {
			// The guarded update bd update --persistent runs, so the move
			// takes the update's one end-of-update mint rather than
			// MoveIssuePersistenceInTx's own.
			ops, err := embeddeddolt.NewIssueOperations(store)
			if err != nil {
				return err
			}
			result, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "actor", IssueID: id, Patch: publicops.IssuePatch{
				Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModePersistent},
			}})
			if err == nil && !result.Changed {
				err = fmt.Errorf("persistence move of %s reported no change", id)
			}
			return err
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
			var gen sql.NullInt64
			if err := kit.QueryScalar(ctx,
				"SELECT participation_generation FROM issues WHERE id = ?", []any{id}, &gen); err != nil {
				return nil, err
			}
			if !gen.Valid {
				return nil, nil
			}
			return &gen.Int64, nil
		},
	}
}
