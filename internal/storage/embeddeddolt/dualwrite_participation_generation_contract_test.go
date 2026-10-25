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
	t.Run("FlagOffSkipsUpdateOnParticipatingRecord", func(t *testing.T) {
		conformance.RunParticipationGenerationFlagOffSkipsUpdateOnParticipatingRecord(t, ctx, fixture)
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
	t.Run("DemotedLegacyRowStaysLegacyAcrossPromotion", func(t *testing.T) {
		conformance.RunParticipationGenerationDemotedLegacyRowStaysLegacyAcrossPromotion(t, ctx, fixture)
	})
	t.Run("DemotedLegacyRowStaysLegacyAcrossPersistenceMove", func(t *testing.T) {
		conformance.RunParticipationGenerationDemotedLegacyRowStaysLegacyAcrossPersistenceMove(t, ctx, fixture)
	})
	t.Run("DemotedParticipatingRowKeepsItsHistoryAcrossPromotion", func(t *testing.T) {
		conformance.RunParticipationGenerationDemotedParticipatingRowKeepsItsHistoryAcrossPromotion(t, ctx, fixture)
	})
	t.Run("DemotedParticipatingRowKeepsItsHistoryAcrossPersistenceMove", func(t *testing.T) {
		conformance.RunParticipationGenerationDemotedParticipatingRowKeepsItsHistoryAcrossPersistenceMove(t, ctx, fixture)
	})
	t.Run("WispFlagClearMintsNothing", func(t *testing.T) {
		conformance.RunParticipationGenerationWispFlagClearMintsNothing(t, ctx, fixture)
	})
	t.Run("WispRootSquashMintsNothing", func(t *testing.T) {
		conformance.RunParticipationGenerationWispRootSquashMintsNothing(t, ctx, fixture)
	})
	t.Run("UnflaggedWispCreateMintsNothing", func(t *testing.T) {
		conformance.RunParticipationGenerationUnflaggedWispCreateMintsNothing(t, ctx, fixture)
	})
	t.Run("NoHistoryIssueCreateDeclaresParticipation", func(t *testing.T) {
		conformance.RunParticipationGenerationNoHistoryIssueCreateDeclaresParticipation(t, ctx, fixture)
	})
	t.Run("LegacyNoHistoryIssueStaysLegacy", func(t *testing.T) {
		conformance.RunParticipationGenerationLegacyNoHistoryIssueStaysLegacy(t, ctx, fixture)
	})
	t.Run("ReadReportsExactlyTheParticipatingRows", func(t *testing.T) {
		conformance.RunParticipationGenerationReadReportsExactlyTheParticipatingRows(t, ctx, fixture)
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
	if fixture.MoveEphemeral == nil {
		t.Error("ParticipationGenerationFixture.MoveEphemeral is nil")
	}
	if fixture.ClearWispFlag == nil {
		t.Error("ParticipationGenerationFixture.ClearWispFlag is nil")
	}
	if fixture.SquashWispRoot == nil {
		t.Error("ParticipationGenerationFixture.SquashWispRoot is nil")
	}
	if fixture.CreateUnflaggedWisp == nil {
		t.Error("ParticipationGenerationFixture.CreateUnflaggedWisp is nil")
	}
	if fixture.CreateNoHistoryIssue == nil {
		t.Error("ParticipationGenerationFixture.CreateNoHistoryIssue is nil")
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
	if fixture.InWispsTable == nil {
		t.Error("ParticipationGenerationFixture.InWispsTable is nil")
	}
	if fixture.ParticipatingIssueIDs == nil {
		t.Error("ParticipationGenerationFixture.ParticipatingIssueIDs is nil")
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
	movePersistence := func(ctx context.Context, id string, mode publicops.PersistenceMode) error {
		ops, err := embeddeddolt.NewIssueOperations(store)
		if err != nil {
			return err
		}
		result, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "actor", IssueID: id, Patch: publicops.IssuePatch{
			Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: mode},
		}})
		if err == nil && !result.Changed {
			err = fmt.Errorf("persistence move of %s to %s reported no change", id, mode)
		}
		return err
	}
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
			return movePersistence(ctx, id, publicops.PersistenceModePersistent)
		},
		MoveEphemeral: func(ctx context.Context, id string) error {
			// The guarded update bd update --ephemeral runs.
			return movePersistence(ctx, id, publicops.PersistenceModeEphemeral)
		},
		ClearWispFlag: func(ctx context.Context, id string) error {
			return store.UpdateIssue(ctx, id, map[string]any{"wisp": false}, "actor")
		},
		SquashWispRoot: func(ctx context.Context, id string) error {
			// bd mol squash's own shape: the root's close and its flag clear
			// in the one transaction the squash commits.
			return store.RunInTransaction(ctx, "squash "+id, func(tx storage.Transaction) error {
				if err := tx.CloseIssue(ctx, id, "squashed", "actor", ""); err != nil {
					return err
				}
				return tx.UpdateIssue(ctx, id, map[string]any{"wisp": false}, "actor")
			})
		},
		CreateUnflaggedWisp: func(ctx context.Context, id string) error {
			// Import's "wisp_plane" marker: the override, not the flags, puts
			// the row in the wisps table.
			wispPlane := true
			return store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
				WispPlaneOverride: &wispPlane,
			}}, "actor", storage.BatchCreateOptions{SkipPrefixValidation: true})
		},
		CreateNoHistoryIssue: func(ctx context.Context, id string) error {
			// Import's promoted-no-history shape (bd-r9uce): the flag stays on
			// the row, and the override pins it to the issues table.
			durable := false
			return store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
				NoHistory: true, WispPlaneOverride: &durable,
			}}, "actor", storage.BatchCreateOptions{SkipPrefixValidation: true})
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
		InWispsTable: func(ctx context.Context, id string) (bool, error) {
			var rows int
			if err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{id}, &rows); err != nil {
				return false, err
			}
			return rows > 0, nil
		},
		// Through the capability assertion cmd/bd makes on the unwrapped store,
		// never the concrete method set.
		ParticipatingIssueIDs: func(ctx context.Context, ids []string) (map[string]struct{}, error) {
			participation, ok := any(store).(storage.HistoryParticipation)
			if !ok {
				return nil, fmt.Errorf("%T does not implement storage.HistoryParticipation", store)
			}
			return participation.ParticipatingIssueIDs(ctx, ids)
		},
	}
}
