package uow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestSweeperContract runs the Sweeper contract against the unit-of-work
// provider — the one implementation that does not run
// internal/storage/issueops.SweepInTx. It is the SECOND of two votes, not the
// third: the two store backends share the other body.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global
// and a parallel subtest would delete another subtest's rows — which on this
// role is not a flake but the operation working as documented.
func TestSweeperContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWSweeperFixture(t, ctx, "swp")

	t.Run("RefusesAnUnfilteredDurableSweep", func(t *testing.T) {
		conformance.RunSweeperRefusesAnUnfilteredDurableSweep(t, ctx, fixture)
	})
	t.Run("RefusesAMalformedRequest", func(t *testing.T) {
		conformance.RunSweeperRefusesAMalformedRequest(t, ctx, fixture)
	})
	t.Run("ClearsOneTierAndLeavesTheOther", func(t *testing.T) {
		conformance.RunSweeperClearsOneTierAndLeavesTheOther(t, ctx, fixture)
	})
	t.Run("TreatsALegacyTypedWispAsEphemeralTier", func(t *testing.T) {
		conformance.RunSweeperTreatsALegacyTypedWispAsEphemeralTier(t, ctx, fixture)
	})
	t.Run("WispsPlaneClearsTheWholeWispsTable", func(t *testing.T) {
		conformance.RunSweeperWispsPlaneClearsTheWholeWispsTable(t, ctx, fixture)
	})
	t.Run("WispsPlaneRequiresAFilter", func(t *testing.T) {
		conformance.RunSweeperWispsPlaneRequiresAFilter(t, ctx, fixture)
	})
	t.Run("ProtectsLiveDependents", func(t *testing.T) {
		conformance.RunSweeperProtectsLiveDependents(t, ctx, fixture)
	})
	t.Run("ProtectsTransitiveLiveDependents", func(t *testing.T) {
		conformance.RunSweeperProtectsTransitiveLiveDependents(t, ctx, fixture)
	})
	t.Run("ProtectsLiveDependentsThroughUnselectedRows", func(t *testing.T) {
		conformance.RunSweeperProtectsLiveDependentsThroughUnselectedRows(t, ctx, fixture)
	})
	t.Run("ProtectsLiveDependentsThroughExcludedIntermediaries", func(t *testing.T) {
		conformance.RunSweeperProtectsLiveDependentsThroughExcludedIntermediaries(t, ctx, fixture)
	})
	t.Run("ProtectsLiveDependentsThroughUnselectedChains", func(t *testing.T) {
		conformance.RunSweeperProtectsLiveDependentsThroughUnselectedChains(t, ctx, fixture)
	})
	t.Run("ProtectsLiveDependentsAcrossPlanes", func(t *testing.T) {
		conformance.RunSweeperProtectsLiveDependentsAcrossPlanes(t, ctx, fixture)
	})
	t.Run("LimitTakesTheOldestClosedFirst", func(t *testing.T) {
		conformance.RunSweeperLimitTakesTheOldestClosedFirst(t, ctx, fixture)
	})
	t.Run("LeavesNoHistoryBeadsToTheDurableTier", func(t *testing.T) {
		conformance.RunSweeperLeavesNoHistoryBeadsToTheDurableTier(t, ctx, fixture)
	})
	t.Run("ProtectsPinnedRows", func(t *testing.T) {
		conformance.RunSweeperProtectsPinnedRows(t, ctx, fixture)
	})
	t.Run("HonorsTheCutoffAndThePattern", func(t *testing.T) {
		conformance.RunSweeperHonorsTheCutoffAndThePattern(t, ctx, fixture)
	})
	t.Run("DryRunChangesNothing", func(t *testing.T) {
		conformance.RunSweeperDryRunChangesNothing(t, ctx, fixture)
	})
	t.Run("ProtectsRowsCitedFromAWispComment", func(t *testing.T) {
		conformance.RunSweeperProtectsRowsCitedFromAWispComment(t, ctx, fixture)
	})
	t.Run("ProtectsCitedRows", func(t *testing.T) {
		conformance.RunSweeperProtectsCitedRows(t, ctx, fixture)
	})
	t.Run("EmptyMatchIsZeroAndNil", func(t *testing.T) {
		conformance.RunSweeperEmptyMatchIsZeroAndNil(t, ctx, fixture)
	})
	t.Run("RecordsExactlyOneHistoryEntry", func(t *testing.T) {
		conformance.RunSweeperRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunSweeperDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// TestSweepInUOWAbortsOnALiveDependentDiscoveryReadError pins that every read
// the live-dependent discovery makes past the candidates' own edges is
// required (GH#7043): a failed incoming-edge or status read on a LATER round
// fails the sweep, and nothing is deleted on the strength of the partial
// graph. The graph is root ← middle ← leaf, with only the root a candidate,
// over a stub unit of work whose reads of the middle row can be broken.
func TestSweepInUOWAbortsOnALiveDependentDiscoveryReadError(t *testing.T) {
	boom := errors.New("injected read failure")
	for _, tc := range []struct {
		name   string
		inject func(*sweepDiscoveryUOW)
	}{
		{"durable edges of the unselected row", func(u *sweepDiscoveryUOW) { u.deps.failDurableFor = "t-middle" }},
		{"wisp edges of the unselected row", func(u *sweepDiscoveryUOW) { u.deps.failWispFor = "t-middle" }},
		{"status of the second-level source", func(u *sweepDiscoveryUOW) { u.issues.failScanFor = "t-leaf" }},
		{"custom statuses", func(u *sweepDiscoveryUOW) { u.config.failCustom = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uw := newSweepDiscoveryUOW(boom)
			tc.inject(uw)
			for _, dryRun := range []bool{true, false} {
				result, err := sweepInUOW(context.Background(), uw, issueops.SweepRequest{
					Tier:                  issueops.SweepWispsPlane,
					IDPattern:             "t-*",
					ProtectLiveDependents: true,
					DryRun:                dryRun,
				})
				if !errors.Is(err, boom) {
					t.Fatalf("DryRun=%v: sweepInUOW error = %v, want the injected read failure", dryRun, err)
				}
				if result.Swept != 0 || result.Skipped != (issueops.SweepSkips{}) {
					t.Errorf("DryRun=%v: result = %+v, want the zero result on error", dryRun, result)
				}
			}
			if uw.issues.deletes != 0 {
				t.Errorf("DeleteIssues called %d times after a failed discovery read, want 0", uw.issues.deletes)
			}
		})
	}

	// The same stub with nothing broken protects the root, so the cases above
	// fail on the read and not on some other path.
	uw := newSweepDiscoveryUOW(boom)
	result, err := sweepInUOW(context.Background(), uw, issueops.SweepRequest{
		Tier: issueops.SweepWispsPlane, IDPattern: "t-*", ProtectLiveDependents: true,
	})
	if err != nil || result.Swept != 0 || result.Skipped.LiveDependent != 1 {
		t.Fatalf("unbroken stub: result = %+v, err = %v; want Swept 0, LiveDependent 1", result, err)
	}
}

type sweepDiscoveryUOW struct {
	UnitOfWork
	issues *sweepDiscoveryIssues
	deps   *sweepDiscoveryDeps
	config *sweepDiscoveryConfig
}

func newSweepDiscoveryUOW(boom error) *sweepDiscoveryUOW {
	closedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	edge := func(source, target string) *types.Dependency {
		return &types.Dependency{IssueID: source, DependsOnID: target, Type: types.DepParentChild}
	}
	return &sweepDiscoveryUOW{
		issues: &sweepDiscoveryIssues{
			boom:       boom,
			candidates: []*types.Issue{{ID: "t-root", Status: types.StatusClosed, ClosedAt: &closedAt, Ephemeral: true}},
			live:       map[string]bool{"t-leaf": true},
		},
		deps: &sweepDiscoveryDeps{
			boom: boom,
			wispIncoming: map[string][]*types.Dependency{
				"t-root":   {edge("t-middle", "t-root")},
				"t-middle": {edge("t-leaf", "t-middle")},
			},
		},
		config: &sweepDiscoveryConfig{boom: boom},
	}
}

func (u *sweepDiscoveryUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u *sweepDiscoveryUOW) DependencyUseCase() domain.DependencyUseCase { return u.deps }
func (u *sweepDiscoveryUOW) ConfigUseCase() domain.ConfigUseCase         { return u.config }

type sweepDiscoveryIssues struct {
	domain.IssueUseCase
	boom        error
	candidates  []*types.Issue
	live        map[string]bool
	failScanFor string
	deletes     int
}

func (s *sweepDiscoveryIssues) SearchWispsPlane(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return s.candidates, nil
}

// SearchIssues is only the live-status scan here: the wisps-plane tier reads
// its candidates through SearchWispsPlane.
func (s *sweepDiscoveryIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	var page domain.SearchPage
	for _, id := range filter.IDs {
		if id == s.failScanFor {
			return domain.SearchPage{}, s.boom
		}
		if s.live[id] {
			page.Items = append(page.Items, &types.Issue{ID: id, Status: types.StatusOpen})
		}
	}
	return page, nil
}

func (s *sweepDiscoveryIssues) DeleteIssues(_ context.Context, params domain.DeleteIssuesParams, _ string) (domain.DeleteIssuesResult, error) {
	s.deletes++
	return domain.DeleteIssuesResult{DeletedCount: len(params.IDs)}, nil
}

type sweepDiscoveryDeps struct {
	domain.DependencyUseCase
	boom           error
	wispIncoming   map[string][]*types.Dependency
	failDurableFor string
	failWispFor    string
}

func (d *sweepDiscoveryDeps) ListByIssueIDs(_ context.Context, ids []string, _ domain.DepListFilter) (domain.DepBulkResult, error) {
	for _, id := range ids {
		if id == d.failDurableFor {
			return domain.DepBulkResult{}, d.boom
		}
	}
	return domain.DepBulkResult{}, nil
}

func (d *sweepDiscoveryDeps) ListByWispIDs(_ context.Context, ids []string, _ domain.DepListFilter) (domain.DepBulkResult, error) {
	out := domain.DepBulkResult{Incoming: make(map[string][]*types.Dependency)}
	for _, id := range ids {
		if id == d.failWispFor {
			return domain.DepBulkResult{}, d.boom
		}
		if deps, ok := d.wispIncoming[id]; ok {
			out.Incoming[id] = deps
		}
	}
	return out, nil
}

type sweepDiscoveryConfig struct {
	domain.ConfigUseCase
	boom       error
	failCustom bool
}

func (c *sweepDiscoveryConfig) GetConfig(context.Context, string) (string, error) { return "", nil }

func (c *sweepDiscoveryConfig) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) {
	if c.failCustom {
		return nil, c.boom
	}
	return nil, nil
}

func newUOWSweeperFixture(t *testing.T, ctx context.Context, prefix string) conformance.SweeperFixture {
	t.Helper()
	return newUOWSweeperFixtureOnProvider(t, newUOWRoleFixtureProvider(t, ctx, prefix), prefix)
}

// newUOWSweeperFixtureOnProvider builds the Sweeper fixture over an existing
// provider, for a contract that composes more of its roles beside the sweeper.
func newUOWSweeperFixtureOnProvider(t *testing.T, provider UnitOfWorkProvider, prefix string) conformance.SweeperFixture {
	t.Helper()
	// Through the capability accessor, not NewSweeper: a provider that stopped
	// offering the role is the regression a constructor call would hide.
	source, ok := provider.(SweeperSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Sweeper accessor", provider)
	}
	sweeper, err := source.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.SweeperFixture{
		IssuePrefix:   kit.IssuePrefix,
		Sweeper:       sweeper,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: uowCommitPending(provider),
		// The write half of the same raw-SQL pass-through the cycle-detector
		// wiring uses, inside ONE committing unit of work.
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return "", fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return "seed legacy sweep rows", nil
			})
		},
		AddDependencies: func(ctx context.Context, req issueops.AddDependenciesRequest) error {
			// Through the DependencyEditor ROLE, which routes each edge to its
			// source plane's dependency table itself.
			ds, ok := provider.(DependencyEditorSource)
			if !ok {
				return fmt.Errorf("provider %T does not offer the DependencyEditor accessor", provider)
			}
			editor, err := ds.DependencyEditor()
			if err != nil {
				return err
			}
			_, err = editor.AddDependencies(ctx, req)
			return err
		},
		AddComment: func(ctx context.Context, issueID, author, text string) error {
			// Through the Commenter ROLE, which resolves the plane itself, so
			// the case can cite from a wisp's comment without knowing how this
			// backend reaches wisp_comments.
			cs, ok := provider.(CommenterSource)
			if !ok {
				return fmt.Errorf("provider %T does not offer the Commenter accessor", provider)
			}
			commenter, err := cs.Commenter()
			if err != nil {
				return err
			}
			_, err = commenter.AddComment(ctx, issueops.AddCommentRequest{
				IssueID: issueID, Author: author, Text: text,
			})
			return err
		},
	}
}
