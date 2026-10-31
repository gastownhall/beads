package uow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestHeartbeaterContract runs the Heartbeater contract against the
// unit-of-work provider, which reaches internal/storage/issueops.
// ExecuteHeartbeatInTx through domain.IssueUseCase.RenewLease inside one unit
// of work committed with RunTxScoped: the SQL-only form for a refresh, the
// scoped commit for a re-arm.
func TestHeartbeaterContract(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fixture := newUOWHeartbeaterFixture(t, ctx, "hbc")
	t.Run("RefreshesTheHoldersLease", func(t *testing.T) {
		conformance.RunHeartbeaterRefreshesTheHoldersLease(t, ctx, fixture)
	})
	t.Run("ChangesNothingOnTheRow", func(t *testing.T) {
		conformance.RunHeartbeaterChangesNothingOnTheRow(t, ctx, fixture)
	})
	t.Run("AGuardedWriteWithThePreHeartbeatRevisionStillWins", func(t *testing.T) {
		conformance.RunHeartbeaterAGuardedWriteWithThePreHeartbeatRevisionStillWins(t, ctx, fixture)
	})
	t.Run("RefusesAForeignHolder", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAForeignHolder(t, ctx, fixture)
	})
	t.Run("RefusesAfterClose", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterClose(t, ctx, fixture)
	})
	t.Run("RefusesAfterRelease", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterRelease(t, ctx, fixture)
	})
	t.Run("RefusesAfterReclaim", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterReclaim(t, ctx, fixture)
	})
	t.Run("RefusesAfterStatusMovedOffInProgress", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAfterStatusMovedOffInProgress(t, ctx, fixture)
	})
	t.Run("RefusesAWisp", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAWisp(t, ctx, fixture)
	})
	t.Run("RearmsAHandDoledClaim", func(t *testing.T) {
		conformance.RunHeartbeaterRearmsAHandDoledClaim(t, ctx, fixture)
	})
	t.Run("AnswersNotFoundForAnAbsentID", func(t *testing.T) {
		conformance.RunHeartbeaterAnswersNotFoundForAnAbsentID(t, ctx, fixture)
	})
	t.Run("RefusesABlankOrOverLongActor", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesABlankOrOverLongActor(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheRequest", func(t *testing.T) {
		conformance.RunHeartbeaterDoesNotMutateTheRequest(t, ctx, fixture)
	})
	t.Run("CommitsOnlyItsOwnTables", func(t *testing.T) {
		conformance.RunHeartbeaterCommitsOnlyItsOwnTables(t, ctx, fixture)
	})
	t.Run("ASameSecondHeartbeatIsNotARearm", func(t *testing.T) {
		conformance.RunHeartbeaterASameSecondHeartbeatIsNotARearm(t, ctx, fixture)
	})
	t.Run("RefusesAnOrphanedLeaseOnAReleasedIssue", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAReleasedIssue(t, ctx, fixture)
	})
	t.Run("RefusesAnOrphanedLeaseOnAClosedIssue", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAClosedIssue(t, ctx, fixture)
	})
	t.Run("RefusesAnOrphanedLeaseOnAnIssueSomeoneElseHolds", func(t *testing.T) {
		conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAnIssueSomeoneElseHolds(t, ctx, fixture)
	})
}

// TestHeartbeaterRearmKeepsAConcurrentWrite is the LatentLabsSpace/NEXUS#92
// regression for the re-arm's scoped commit: another session commits a change
// to a different issues row while the re-arm's transaction is open, and the
// Dolt commit the re-arm makes must stage issues as committed, with that
// change in it, not as the re-arm's start snapshot. DOLT_ADD stages a whole
// table from the session's transaction root, so staging inside the open
// transaction did the latter: HEAD then disagreed with the rows every reader
// sees, for a write still only in the working set (batch mode, or the window
// before the writer's own Dolt commit). ScopedCommitter commits the SQL
// transaction first.
//
// Neither concurrent write may take the journal writer lock: the re-arm's
// session holds it for the whole window, so a locked writer (a committing
// RunTx, journal on or off) waits it out and then reclaims the re-arm's session
// as a silent holder. A locked writer cannot land in the window at all; the
// writers that still can are the unlocked ones. The committed one is therefore
// a raw session on the provider's pool that writes and mints its own Dolt
// commit, as the unlocked pull/merge path does; the working-set one is an
// ephemeral unit of work, which takes no lock.
func TestHeartbeaterRearmKeepsAConcurrentWrite(t *testing.T) {
	ctx := t.Context()
	const prefix = "hbw"
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	kit := newUOWRoleFixtureKit(provider, prefix)
	sqlProvider, ok := provider.(*doltSQLProvider)
	if !ok {
		t.Fatalf("provider %T is not a *doltSQLProvider", provider)
	}
	lifecycle, err := provider.(IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	const holder = "hb-holder"
	for _, tc := range []struct {
		name  string
		write func(ctx context.Context, id, title string) error
	}{
		{"a write with its own dolt commit", func(ctx context.Context, id, title string) error {
			conn, err := sqlProvider.db.Conn(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", title, id); err != nil {
				return err
			}
			if err := schema.DrainCall(ctx, conn, "CALL DOLT_ADD(?)", "issues"); err != nil {
				return err
			}
			return schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?)", "bd: retitle "+id)
		}},
		{"a write only in the working set", func(ctx context.Context, id, title string) error {
			_, err := RunTxEphemeral(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
				_, err := uw.RawSQLUseCase().Exec(ctx, "UPDATE issues SET title = ? WHERE id = ?", title, id)
				return struct{}{}, err
			})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suffix := strings.ReplaceAll(tc.name, " ", "-")
			doled, bystander := prefix+"-doled-"+suffix, prefix+"-bystander-"+suffix
			for _, id := range []string{doled, bystander} {
				issue := &types.Issue{ID: id, Title: "seed " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if err := kit.CreateIssue(ctx, issue, "seeder"); err != nil {
					t.Fatalf("seed %s: %v", id, err)
				}
			}
			if _, err := lifecycle.Update(ctx, publicops.UpdateRequest{
				Actor: holder, IssueID: doled,
				Patch: publicops.IssuePatch{
					Status:   publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
					Assignee: publicops.Field[string]{Set: true, Value: holder},
				},
			}); err != nil {
				t.Fatalf("hand-dole %s: %v", doled, err)
			}

			const retitled = "retitled while the re-arm was open"
			wrote := false
			heartbeater, err := NewHeartbeater(&beforeScopedCommitProvider{
				UnitOfWorkProvider: provider,
				before: func(ctx context.Context) error {
					if wrote {
						return nil
					}
					wrote = true
					return tc.write(ctx, bystander, retitled)
				},
			})
			if err != nil {
				t.Fatalf("NewHeartbeater: %v", err)
			}
			result, err := heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: holder, IssueID: doled})
			if err != nil {
				t.Fatalf("Heartbeat: %v", err)
			}
			if !result.Rearmed || !wrote {
				t.Fatalf("result = %+v, concurrent write ran = %v: the case needs a re-arm with a write inside its window", result, wrote)
			}

			for _, at := range []string{"the working set", "HEAD"} {
				query := "SELECT title FROM issues WHERE id = ?"
				if at == "HEAD" {
					query = "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?"
				}
				var title string
				if err := kit.QueryScalar(ctx, query, []any{bystander}, &title); err != nil {
					t.Fatalf("read %s title at %s: %v", bystander, at, err)
				}
				if title != retitled {
					t.Errorf("%s title at %s = %q, want %q: the re-arm's Dolt commit staged issues from its start snapshot", bystander, at, title, retitled)
				}
			}
			// The re-arm itself is committed: its reminted row_lock is at HEAD
			// (events is dolt_ignored, so the issues row is what its commit
			// carries).
			var working, head int64
			if err := kit.QueryScalar(ctx, "SELECT row_lock FROM issues WHERE id = ?", []any{doled}, &working); err != nil {
				t.Fatalf("read %s row_lock: %v", doled, err)
			}
			if err := kit.QueryScalar(ctx, "SELECT row_lock FROM issues AS OF 'HEAD' WHERE id = ?", []any{doled}, &head); err != nil {
				t.Fatalf("read %s row_lock at HEAD: %v", doled, err)
			}
			var commits int
			if err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", []any{"bd: re-arm lease " + doled}, &commits); err != nil {
				t.Fatalf("count re-arm commits: %v", err)
			}
			if head != working || commits != 1 {
				t.Errorf("%s row_lock at HEAD = %d (working set %d), re-arm commits = %d, want the re-arm in one Dolt commit", doled, head, working, commits)
			}
		})
	}
}

// beforeScopedCommitProvider runs before ahead of each scoped commit of the
// units of work it opens: a way into the window between a transaction's
// writes and its commit.
type beforeScopedCommitProvider struct {
	UnitOfWorkProvider
	before func(context.Context) error
}

func (p *beforeScopedCommitProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &beforeScopedCommitUOW{UnitOfWork: uw, before: p.before}, nil
}

type beforeScopedCommitUOW struct {
	UnitOfWork
	before func(context.Context) error
}

func (u *beforeScopedCommitUOW) Unwrap() UnitOfWork { return u.UnitOfWork }

func (u *beforeScopedCommitUOW) CommitScoped(ctx context.Context, tables []string, message string) error {
	if err := u.before(ctx); err != nil {
		return err
	}
	return commitScoped(ctx, u.UnitOfWork, tables, message)
}

// TestHeartbeaterRefusesBeforeOpeningAUOW: a malformed heartbeat is a
// deterministic validation failure and must not cost a database connection to
// discover, as on the dolt and embedded-dolt legs.
func TestHeartbeaterRefusesBeforeOpeningAUOW(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.HeartbeatRequest
	}{
		{"no actor", publicops.HeartbeatRequest{IssueID: "x"}},
		{"no id", publicops.HeartbeatRequest{Actor: "worker"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &mockUnitOfWorkProvider{newUOWErr: errors.New("unexpected unit-of-work open")}
			heartbeater, err := NewHeartbeater(provider)
			if err != nil {
				t.Fatalf("NewHeartbeater: %v", err)
			}
			if _, err := heartbeater.Heartbeat(context.Background(), tc.request); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
			if provider.newUOWCalls != 0 {
				t.Errorf("an invalid request opened %d units of work", provider.newUOWCalls)
			}
		})
	}
}

// newUOWHeartbeaterFixture takes every role through the provider's own
// capability accessors, and the hooked heartbeater off the notifying provider
// the proxied CLI route wraps around its provider.
func newUOWHeartbeaterFixture(t *testing.T, ctx context.Context, prefix string) conformance.HeartbeaterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	source, ok := provider.(HeartbeaterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Heartbeater accessor", provider)
	}
	heartbeater, err := source.Heartbeater()
	if err != nil {
		t.Fatalf("Heartbeater(): %v", err)
	}
	claimer, err := provider.(IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	lifecycle, err := provider.(IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	releaser, err := provider.(ReleaserSource).Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	reclaimer, err := provider.(LeaseReclaimerSource).LeaseReclaimer()
	if err != nil {
		t.Fatalf("LeaseReclaimer(): %v", err)
	}
	configurer, ok := provider.(storage.EventsJournalConfigurer)
	if !ok {
		t.Fatalf("provider %T does not implement storage.EventsJournalConfigurer", provider)
	}
	versionConfigurer, ok := provider.(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("provider %T does not implement storage.VersionedHistoryConfigurer", provider)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	hooked := heartbeater
	if runner != nil {
		notifying, ok := NewNotifyingProvider(provider, Sinks{Hook: runner}).(HeartbeaterSource)
		if !ok {
			t.Fatal("the notifying provider does not offer the Heartbeater accessor")
		}
		if hooked, err = notifying.Heartbeater(); err != nil {
			t.Fatalf("hooked Heartbeater(): %v", err)
		}
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.HeartbeaterFixture{
		IssuePrefix:    kit.IssuePrefix,
		Heartbeater:    heartbeater,
		CreateIssue:    kit.CreateIssue,
		CreateWisp:     kit.CreateWisp,
		Claimer:        claimer,
		Lifecycle:      lifecycle,
		Releaser:       releaser,
		LeaseReclaimer: reclaimer,
		QueryScalar:    kit.QueryScalar,
		CountHistory:   kit.CountHistory,
		ExecUncommitted: func(ctx context.Context, statements []conformance.SQLStatement) error {
			_, err := RunTxEphemeral(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return struct{}{}, fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return struct{}{}, nil
			})
			return err
		},
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return "", fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return "seed heartbeater rows", nil
			})
		},
		SetJournalEnabled:          configurer.SetEventsJournalEnabled,
		SetVersionedHistoryEnabled: versionConfigurer.SetVersionedHistoryEnabled,
		HookedHeartbeater:          hooked,
		FiredUpdates:               fired,
	}
}
