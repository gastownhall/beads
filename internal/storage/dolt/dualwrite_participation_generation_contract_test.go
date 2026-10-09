package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestParticipationGenerationFence runs the design §16.2(b) write-fence
// contract (backend/conformance/dualwrite_participation_generation_contract.go)
// against the dolt leg. Like TestDualWriteContract, this is an engine check
// rather than an independent per-leg vote: all three legs share
// RecordVersionInTx's one fence check, and the dolt leg is where a fence
// check that ran outside the mutation's own transaction, or that missed a
// call site, would have somewhere concrete to go wrong.
func TestParticipationGenerationFence(t *testing.T) {
	fixture, ctx, cleanup := newDoltParticipationGenerationFixture(t, "pg")
	defer cleanup()

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
	t.Run("WispPromotionBeginsHistory", func(t *testing.T) {
		conformance.RunParticipationGenerationWispPromotionBeginsHistory(t, ctx, fixture)
	})
	t.Run("WispPersistenceMoveBeginsHistory", func(t *testing.T) {
		conformance.RunParticipationGenerationWispPersistenceMoveBeginsHistory(t, ctx, fixture)
	})
}

// TestParticipationGenerationFence_FenceIgnoresSchemaSkewHatch pins the same
// design §16.2(c) case as TestParticipationGenerationFence's other subtests,
// but as its own non-parallel top-level test against a dedicated database
// rather than a fifth t.Run under it: this case's conformance function calls
// t.Setenv, which Go forbids anywhere in a parallel test's tree, and the
// fixture above is built via setupTestStore, which calls t.Parallel() (the
// two combined panic the whole test binary). schema_skew_test.go's
// TestCheckForwardDrift_EscapeHatch_ReturnsNil hits the identical constraint
// and solves it the same way: a store built outside setupTestStore.
func TestParticipationGenerationFence_FenceIgnoresSchemaSkewHatch(t *testing.T) {
	fixture, ctx, cleanup := newDoltParticipationGenerationFixtureSerial(t, "pgh")
	defer cleanup()
	conformance.RunParticipationGenerationFenceIgnoresSchemaSkewHatch(t, ctx, fixture)
}

// TestParticipationGenerationFixtureKitIsWired is the explicit per-leg
// guardrail design §8.5 calls for in place of AST auto-discovery — see the
// dual-write contract's TestDualWriteFixtureKitIsWired for why the guardrail
// is needed even though TestEveryLegWiresEveryRoleContract's scan already
// enumerates and confirms wiring for every RunParticipationGenerationXxx
// entrypoint.
func TestParticipationGenerationFixtureKitIsWired(t *testing.T) {
	fixture, _, cleanup := newDoltParticipationGenerationFixture(t, "pgk")
	defer cleanup()

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

func newDoltParticipationGenerationFixture(t *testing.T, prefix string) (conformance.ParticipationGenerationFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the type assertion `bd serve` makes, never the concrete method
	// set — same discipline as newDoltDualWriteFixture above.
	configurer, ok := any(store).(storage.VersionedHistoryConfigurer)
	if !ok {
		cancel()
		storeCleanup()
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", store)
	}
	fixture := buildParticipationGenerationFixture(store, configurer, prefix)
	return fixture, ctx, func() {
		// Dual-write is instance-scoped, and this store outlives the fixture
		// in the shared-database harness — same reasoning as
		// newDoltDualWriteFixture's cleanup above.
		configurer.SetVersionedHistoryEnabled(false)
		cancel()
		storeCleanup()
	}
}

// newDoltParticipationGenerationFixtureSerial is
// newDoltParticipationGenerationFixture's non-parallel twin, for the one case
// (TestParticipationGenerationFence_FenceIgnoresSchemaSkewHatch) that needs
// t.Setenv: a dedicated, freshly created database instead of a branch on the
// shared test database, built the same way schema_skew_test.go's
// TestCheckForwardDrift_EscapeHatch_ReturnsNil builds its store, so the
// caller never triggers setupTestStore's t.Parallel().
func newDoltParticipationGenerationFixtureSerial(t *testing.T, prefix string) (conformance.ParticipationGenerationFixture, context.Context, func()) {
	t.Helper()
	skipIfNoDolt(t)
	ctx, cancel := testContext(t)

	store, err := New(ctx, &Config{
		Path:            t.TempDir(),
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        uniqueTestDBName(t),
		CreateIfMissing: true,
	})
	if err != nil {
		cancel()
		t.Fatalf("failed to create Dolt store: %v", err)
	}
	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
		store.Close()
		cancel()
		t.Fatalf("failed to initialize schema: %v", err)
	}
	// A standalone database starts with an empty config table — unlike
	// setupTestStore's shared database, which testmain_test.go seeds once.
	// CreateIssue's ReadConfigPrefix check requires this row to exist at all;
	// it does not need to match the ID prefix used elsewhere in the suite.
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		store.Close()
		cancel()
		t.Fatalf("set issue_prefix to %q: %v", prefix, err)
	}
	configurer, ok := any(store).(storage.VersionedHistoryConfigurer)
	if !ok {
		store.Close()
		cancel()
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", store)
	}
	fixture := buildParticipationGenerationFixture(store, configurer, prefix)
	return fixture, ctx, func() {
		configurer.SetVersionedHistoryEnabled(false)
		cancel()
		// Do not DROP DATABASE here: rapid CREATE/DROP cycles can crash the
		// Dolt testcontainer, same reasoning as the escape-hatch test's own
		// cleanup. The random test database is discarded with the container.
		store.Close()
	}
}

// buildParticipationGenerationFixture wires a ParticipationGenerationFixture
// against an already-constructed, already-schema-initialized store — the
// part newDoltParticipationGenerationFixture and its serial twin share,
// factored out so the t.Parallel()-vs-t.Setenv split above does not fork the
// fixture wiring itself into two copies that could drift.
func buildParticipationGenerationFixture(store *DoltStore, configurer storage.VersionedHistoryConfigurer, prefix string) conformance.ParticipationGenerationFixture {
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
			ops, err := NewIssueOperations(store)
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
			err := store.db.QueryRowContext(ctx, `SELECT current_revision FROM issues WHERE id = ?`, id).
				Scan(&revision)
			return revision, err
		},
		VersionRowCount: func(ctx context.Context, id string) (int, error) {
			var count int
			err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?`, id).
				Scan(&count)
			return count, err
		},
		ParticipationGeneration: func(ctx context.Context, id string) (*int64, error) {
			var gen sql.NullInt64
			if err := store.db.QueryRowContext(ctx,
				`SELECT participation_generation FROM issues WHERE id = ?`, id).Scan(&gen); err != nil {
				return nil, err
			}
			if !gen.Valid {
				return nil, nil
			}
			return &gen.Int64, nil
		},
	}
}
