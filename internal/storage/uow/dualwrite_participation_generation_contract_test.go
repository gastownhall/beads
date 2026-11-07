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

// TestParticipationGenerationUpsertCreate pins the write fence (design
// §16.2b) on the unit-of-work create that is not CreateOnly, the default for
// bd batch, bd todo, tracker sync and mol port. That create's insert is an
// upsert, so db.Insert has to tell a new row (create-shaped mint: stamp the
// column) from an existing one the upsert resolved to (update-shaped: fenced
// when legacy). TestParticipationGenerationFence cannot see that choice: its
// Mutate is CreateOnly, and so is every served create (createParams), so no
// other leg reaches this branch at all.
//
// PromoteOverDualPresenceKeepsLegacy asks the same question of
// PromoteFromEphemeralInTx's upsert: a wisp whose id the issues plane already
// holds (GH#4455's dual presence, written the way a store corrupted before
// that fix holds it) is promoted onto that existing row.
// WispOverLegacyIssuesRowKeepsItLegacy asks it of the wisps-table half of the
// same db.Insert (bd mol port's CreateWisp): that insert never creates an
// issues row, so the only issues row its mint can reach is a twin that
// already held the id.
func TestParticipationGenerationUpsertCreate(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "pgu")
	fixture := uowParticipationGenerationFixtureFor(t, provider, "pgu")

	setFlag := func(t *testing.T, enabled bool) {
		t.Helper()
		if err := fixture.SetFlag(ctx, enabled); err != nil {
			t.Fatalf("setting versioned history to %v: %v", enabled, err)
		}
	}
	upsert := func(t *testing.T, id, title string) {
		t.Helper()
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
				Issue: &types.Issue{
					ID: id, Title: title, IssueType: types.TypeTask, Status: types.StatusOpen,
				},
				ExplicitID: id,
			}, "actor")
			return "upsert " + id, err
		}); err != nil {
			t.Fatalf("upsert-creating %s: %v", id, err)
		}
	}
	requireState := func(t *testing.T, id string, wantStamped bool, wantVersions int, why string) {
		t.Helper()
		gen, err := fixture.ParticipationGeneration(ctx, id)
		if err != nil {
			t.Fatalf("reading participation_generation for %s: %v", id, err)
		}
		if stamped := gen != nil; stamped != wantStamped {
			t.Errorf("participation_generation for %s stamped=%v, want %v: %s", id, stamped, wantStamped, why)
		}
		versions, err := fixture.VersionRowCount(ctx, id)
		if err != nil {
			t.Fatalf("counting version rows for %s: %v", id, err)
		}
		if versions != wantVersions {
			t.Errorf("version row count for %s = %d, want %d: %s", id, versions, wantVersions, why)
		}
	}

	t.Run("NewRowIsCreateShaped", func(t *testing.T) {
		setFlag(t, true)
		id := "pgu-new"
		upsert(t, id, "t-"+id)
		requireState(t, id, true, 1, "an upsert that inserted the row is a create, so it stamps the column and begins the history")
	})
	t.Run("LegacyRowStaysLegacy", func(t *testing.T) {
		setFlag(t, false)
		id := "pgu-legacy"
		upsert(t, id, "t-"+id)
		requireState(t, id, false, 0, "a create with history off leaves the row legacy")
		setFlag(t, true)
		upsert(t, id, "resynced-"+id)
		requireState(t, id, false, 0, "an upsert that resolved to a legacy row is update-shaped and fenced; stamping it would promote a legacy record (R2.3)")
	})
	t.Run("StampedRowMintsOnce", func(t *testing.T) {
		setFlag(t, true)
		id := "pgu-stamped"
		upsert(t, id, "t-"+id)
		upsert(t, id, "resynced-"+id)
		requireState(t, id, true, 2, "an upsert that resolved to a participating row mints exactly one more version")
	})
	t.Run("PromoteOverDualPresenceKeepsLegacy", func(t *testing.T) {
		setFlag(t, false)
		id := "pgu-dual"
		if err := fixture.Mutate(ctx, id); err != nil {
			t.Fatalf("creating legacy %s: %v", id, err)
		}
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.RawSQLUseCase().Exec(ctx,
				"INSERT INTO wisps (id, title, status, priority, issue_type, ephemeral, created_at, updated_at) "+
					"SELECT id, title, status, priority, issue_type, 1, created_at, updated_at FROM issues WHERE id = ?", id)
			return "plant wisp twin " + id, err
		}); err != nil {
			t.Fatalf("planting a wisps-table twin of %s: %v", id, err)
		}
		setFlag(t, true)
		if err := fixture.PromoteWisp(ctx, id); err != nil {
			t.Fatalf("promoting %s: %v", id, err)
		}
		if inWisps, err := fixture.InWispsTable(ctx, id); err != nil {
			t.Fatalf("checking the wisps table for %s: %v", id, err)
		} else if inWisps {
			t.Fatalf("%s is still in the wisps table after its promote", id)
		}
		requireState(t, id, false, 0, "a promote whose upsert resolved to an existing legacy issues row is update-shaped and fenced; stamping it would promote a legacy record (R2.3)")
	})
	t.Run("WispOverLegacyIssuesRowKeepsItLegacy", func(t *testing.T) {
		setFlag(t, false)
		id := "pgu-wisp-twin"
		upsert(t, id, "t-"+id)
		requireState(t, id, false, 0, "a create with history off leaves the row legacy")
		setFlag(t, true)
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.IssueUseCase().CreateWisp(ctx, domain.CreateIssueParams{
				Issue: &types.Issue{
					ID: id, Title: "wisp-" + id, IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
				},
				ExplicitID: id,
			}, "actor")
			return "wisp upsert " + id, err
		}); err != nil {
			t.Fatalf("wisp-creating %s over its issues row: %v", id, err)
		}
		if inWisps, err := fixture.InWispsTable(ctx, id); err != nil {
			t.Fatalf("checking the wisps table for %s: %v", id, err)
		} else if !inWisps {
			t.Fatalf("%s is not in the wisps table after its wisp create; the case needs both planes to hold the id", id)
		}
		requireState(t, id, false, 0, "a wisps-table insert never creates the issues row the seam snapshots, so its mint is update-shaped and fenced; stamping that legacy twin would promote a legacy record (R2.3)")
	})
}

func newUOWParticipationGenerationFixture(t *testing.T, ctx context.Context, prefix string) conformance.ParticipationGenerationFixture {
	t.Helper()
	return uowParticipationGenerationFixtureFor(t, newUOWRoleFixtureProvider(t, ctx, prefix), prefix)
}

// uowParticipationGenerationFixtureFor is newUOWParticipationGenerationFixture
// over a provider the caller already holds, for a test that also drives that
// provider directly.
func uowParticipationGenerationFixtureFor(t *testing.T, provider UnitOfWorkProvider, prefix string) conformance.ParticipationGenerationFixture {
	t.Helper()
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
		MoveEphemeral: func(ctx context.Context, id string) error {
			// ApplyUpdate's persistence step, as MovePersistent's.
			ephemeral := types.PersistenceModeEphemeral
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				_, err := uw.IssueUseCase().ApplyUpdate(ctx, id, domain.UpdateSpec{Persistence: &ephemeral}, "actor")
				return "move ephemeral " + id, err
			})
		},
		ClearWispFlag: func(ctx context.Context, id string) error {
			// UpdateWisp, as the molecule writer routes bd mol squash's flag
			// clear for a row it found on the wisp plane.
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "clear wisp flag " + id, uw.IssueUseCase().UpdateWisp(ctx, id,
					map[string]any{"wisp": false}, "actor")
			})
		},
		SquashWispRoot: func(ctx context.Context, id string) error {
			// The proxied bd mol squash's shape: CloseWisp then UpdateWisp in
			// the one unit of work the squash commits.
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				if _, err := uw.IssueUseCase().CloseWisp(ctx, id, domain.CloseIssueParams{Reason: "squashed"}, "actor"); err != nil {
					return "", err
				}
				return "squash " + id, uw.IssueUseCase().UpdateWisp(ctx, id,
					map[string]any{"wisp": false}, "actor")
			})
		},
		CreateUnflaggedWisp: func(ctx context.Context, id string) error {
			// CreateWisp routes by the call, so neither flag is needed to land
			// the row in the wisps table.
			return kit.CreateWisp(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
			}, "actor")
		},
		CreateNoHistoryIssue: func(ctx context.Context, id string) error {
			// CreateIssue routes by the call, so the NoHistory flag rides on an
			// issues-table row without moving it to the wisps table.
			return kit.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen, NoHistory: true,
			}, "actor")
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
		InWispsTable: func(ctx context.Context, id string) (bool, error) {
			var rows int
			if err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{id}, &rows); err != nil {
				return false, err
			}
			return rows > 0, nil
		},
		// The unit-of-work route's participation read: one read-only unit of
		// work and its IssueUseCase, the way cmd/bd's pre-enable scan asks.
		ParticipatingIssueIDs: func(ctx context.Context, ids []string) (map[string]struct{}, error) {
			return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (map[string]struct{}, error) {
				return uw.IssueUseCase().ParticipatingIssueIDs(ctx, ids)
			})
		},
	}
}
