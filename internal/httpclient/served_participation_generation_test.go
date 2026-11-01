//go:build cgo

package httpclient

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The ParticipationGenerationFixture contracts (design §16.2b's write fence),
// run through client → in-process bd serve → reference store, the same shape
// as served_dualwrite_history_test.go.
//
// The mutations under test are the CLIENT'S. Mutate is Lifecycle.Create,
// MutateExisting is Lifecycle.Update, MovePersistent is the persistence-mode
// Lifecycle.Update that bd update --persistent sends, and PromoteWisp is
// issueops.Promote over the client's own roles, the one library entry bd
// promote dials over http. On the server those reach ExecuteCreate and the
// guarded update (ExecuteUpdate). The store legs' MutateExisting goes through
// store.UpdateIssue instead, so this leg is the one that runs the fence
// behind the update a connected workspace actually sends.
//
// SetFlag flips the REFERENCE store's versioned-history flag, and
// CurrentRevision, VersionRowCount and ParticipationGeneration read its raw
// columns, as served_dualwrite_history_test.go's fixture does. CreateWisp is a
// seed, so it goes through the reference store (the harness rule in
// served_harness_test.go).
//
// ClearWispFlag, SquashWispRoot and CreateUnflaggedWisp stay nil, and the cases
// that need them stay unwired, along with the one whose subject is a no-history
// issues-table create. None of those mutations has a form on this wire; see
// participationGenerationWireGapWaiverReason in
// internal/storage/leg_contract_wiring_test.go. CreateNoHistoryIssue is bound,
// but only as the reference-store seed of the legacy no-history case.
// InWispsTable reads the reference store's raw table, as the other read hooks
// do, for the round-trip cases' precondition that the client's MoveEphemeral
// really put the row in the wisps table.
//
// The fixture gets its OWN env (newServedEnv), never the shared composition,
// for the reason served_dualwrite_history_test.go gives: the flag is a knob on
// the reference store instance the server's roles are bound to.
func newServedParticipationGenerationFixture(t *testing.T, prefix string) conformance.ParticipationGenerationFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	configurer, ok := any(env.reference).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", env.reference)
	}
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	reader, err := env.subject.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	commenter, err := env.subject.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	// The flag is instance-scoped and outlives nothing past this env, but turn
	// it off anyway so a later hook on the same env never inherits it.
	t.Cleanup(func() { configurer.SetVersionedHistoryEnabled(false) })

	move := func(ctx context.Context, id string, mode issueops.PersistenceMode) error {
		result, err := lifecycle.Update(ctx, issueops.UpdateRequest{
			Actor:   "writer",
			IssueID: id,
			Patch:   issueops.IssuePatch{Persistence: set(mode)},
		})
		if err == nil && !result.Changed {
			err = fmt.Errorf("persistence move of %s to %s reported no change", id, mode)
		}
		return err
	}

	return conformance.ParticipationGenerationFixture{
		IssuePrefix: env.prefix,
		SetFlag: func(_ context.Context, enabled bool) error {
			configurer.SetVersionedHistoryEnabled(enabled)
			return nil
		},
		Mutate: func(ctx context.Context, id string) error {
			_, err := lifecycle.Create(ctx, issueops.CreateRequest{
				Actor:         "writer",
				ForceIDPrefix: true,
				Issue: &types.Issue{
					ID: id, Title: "t-" + id, Status: types.StatusOpen,
					Priority: 1, IssueType: types.TypeTask,
				},
			})
			return err
		},
		MutateExisting: func(ctx context.Context, id string) error {
			// A different value than Mutate gave it, so the server's
			// DiscardNoopIssueUpdates cannot discard the update before it
			// reaches the fence.
			_, err := lifecycle.Update(ctx, issueops.UpdateRequest{
				Actor:   "writer",
				IssueID: id,
				Patch:   issueops.IssuePatch{Title: set("updated-" + id)},
			})
			return err
		},
		CreateWisp: func(ctx context.Context, id string) error {
			return env.createWisp(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
			}, "actor")
		},
		PromoteWisp: func(ctx context.Context, id string) error {
			promoted, err := issueops.Promote(ctx, issueops.PromoteRoles{
				Reader: reader, Lifecycle: lifecycle, Commenter: commenter,
			}, issueops.PromoteRequest{Actor: "writer", IssueID: id})
			if err != nil {
				return err
			}
			return promoted.CommentErr
		},
		MovePersistent: func(ctx context.Context, id string) error {
			return move(ctx, id, issueops.PersistenceModePersistent)
		},
		// The persistence-mode Lifecycle.Update bd update --ephemeral sends.
		MoveEphemeral: func(ctx context.Context, id string) error {
			return move(ctx, id, issueops.PersistenceModeEphemeral)
		},
		// A SEED, not a subject: the one case wired here that uses it creates
		// the row with the flag OFF, as the precondition for the client's
		// update and repair. It goes through the reference store because no
		// client create can land a no-history row in the issues table (see
		// participationGenerationWireGapWaiverReason), which is also why the
		// case whose subject is this create stays waived.
		CreateNoHistoryIssue: func(ctx context.Context, id string) error {
			durable := false
			return env.reference.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
				NoHistory: true, WispPlaneOverride: &durable,
			}}, "actor", storage.BatchCreateOptions{SkipPrefixValidation: true})
		},
		CurrentRevision: func(ctx context.Context, id string) (int64, error) {
			var revision int64
			err := env.queryScalar(ctx, "SELECT current_revision FROM issues WHERE id = ?", []any{id}, &revision)
			return revision, err
		},
		VersionRowCount: func(ctx context.Context, id string) (int, error) {
			var count int
			err := env.queryScalar(ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{id}, &count)
			return count, err
		},
		ParticipationGeneration: func(ctx context.Context, id string) (*int64, error) {
			var gen sql.NullInt64
			if err := env.queryScalar(ctx,
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
			err := env.queryScalar(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{id}, &rows)
			return rows > 0, err
		},
	}
}

// TestServedParticipationGenerationFence runs every participation-generation
// case this wire can express. Nothing here is parallel, so the schema-skew
// case's t.Setenv can run as a subtest; the in-process server reads the same
// environment.
func TestServedParticipationGenerationFence(t *testing.T) {
	ctx := t.Context()
	fixture := newServedParticipationGenerationFixture(t, "hpg")

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
	t.Run("LegacyNoHistoryIssueStaysLegacy", func(t *testing.T) {
		conformance.RunParticipationGenerationLegacyNoHistoryIssueStaysLegacy(t, ctx, fixture)
	})
}
