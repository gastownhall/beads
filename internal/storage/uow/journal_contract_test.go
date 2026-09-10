package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournalContract runs the Journal contract against the unit-of-work
// provider, which reaches issueops.ReadEventsPageInTx through
// domain.EventsJournalUseCase. All three legs share that one body, so this is
// an ENGINE CHECK rather than a second vote — and here it is a check on the
// composition, which is the part that genuinely differs: this leg is the only
// one whose read runs inside a unit of work it did not open, so a RunTx where
// RunTxRead belongs would commit a read of a dolt_ignored table.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, and two of these cases prune the journal — one to
// nothing — while every case rebaselines off the live head (see
// journal_contract.go's seeding note).
func TestJournalContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWJournalFixture(t, ctx, "jc")

	t.Run("PagesAreSeqAscendingAndSinceExclusive", func(t *testing.T) {
		conformance.RunJournalPagesAreSeqAscendingAndSinceExclusive(t, ctx, fixture)
	})
	t.Run("HeadArrivesWithItsRowsAndDetectsCaughtUp", func(t *testing.T) {
		conformance.RunJournalHeadArrivesWithItsRowsAndDetectsCaughtUp(t, ctx, fixture)
	})
	t.Run("LimitCapsRowsNotHead", func(t *testing.T) {
		conformance.RunJournalLimitCapsRowsNotHead(t, ctx, fixture)
	})
	t.Run("TruncationIsTypedAndNamesTheWindow", func(t *testing.T) {
		conformance.RunJournalTruncationIsTypedAndNamesTheWindow(t, ctx, fixture)
	})
	t.Run("HeadSurvivesAFullPrune", func(t *testing.T) {
		conformance.RunJournalHeadSurvivesAFullPrune(t, ctx, fixture)
	})
	t.Run("EveryMutationKindLandsARow", func(t *testing.T) {
		conformance.RunJournalEveryMutationKindLandsARow(t, ctx, fixture)
	})
}

func newUOWJournalFixture(t *testing.T, ctx context.Context, prefix string) conformance.JournalFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewEventsJournalCursor: a provider
	// that stopped offering the role is the regression a constructor call would
	// hide.
	source, ok := provider.(EventsJournalCursorSource)
	if !ok {
		t.Fatalf("provider %T does not offer the EventsJournalCursor accessor", provider)
	}
	cursor, err := source.EventsJournalCursor()
	if err != nil {
		t.Fatalf("EventsJournalCursor(): %v", err)
	}
	// Activation is the OTHER half, and it is a different interface on purpose:
	// the read role cannot turn itself on, so the fixture reaches for the
	// operator surface the way `bd serve` does.
	configurer, ok := provider.(storage.EventsJournalConfigurer)
	if !ok {
		t.Fatalf("provider %T does not implement storage.EventsJournalConfigurer", provider)
	}
	return conformance.JournalFixture{
		IssuePrefix:       prefix,
		Journal:           cursor,
		SetJournalEnabled: configurer.SetEventsJournalEnabled,
		Prune: func(ctx context.Context, before int64, retainDays, retainRows int) (int64, error) {
			// Ephemeral, exactly as `bd events prune` runs it here: the journal
			// table is dolt_ignored, so the delete has to persist into the
			// working set without minting a Dolt commit.
			return RunTxEphemeral(ctx, provider, func(ctx context.Context, uw UnitOfWork) (int64, error) {
				return uw.EventsJournalUseCase().Prune(ctx, before, retainDays, retainRows)
			})
		},
		Mutations: conformance.JournalMutations{
			Create: func(ctx context.Context, id string) error {
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
			Update: func(ctx context.Context, id string) error {
				return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
					return "update " + id, uw.IssueUseCase().UpdateIssue(ctx, id,
						map[string]any{"title": "renamed " + id}, "actor")
				})
			},
			Close: func(ctx context.Context, id string) error {
				return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
					_, err := uw.IssueUseCase().CloseIssue(ctx, id, domain.CloseIssueParams{Reason: "done"}, "actor")
					return "close " + id, err
				})
			},
			Delete: func(ctx context.Context, id string) error {
				return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
					_, err := uw.IssueUseCase().DeleteIssue(ctx, id, "actor")
					return "delete " + id, err
				})
			},
			AddDependency: func(ctx context.Context, from, to string) error {
				return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
					return "dep add " + from, uw.DependencyUseCase().AddDependency(ctx, &types.Dependency{
						IssueID: from, DependsOnID: to, Type: types.DepBlocks,
					}, "actor")
				})
			},
			RemoveDependency: func(ctx context.Context, from, to string) error {
				return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
					return "dep remove " + from, uw.DependencyUseCase().RemoveDependency(ctx, from, to, "actor")
				})
			},
			Comment: func(ctx context.Context, id, text string) error {
				return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
					_, err := uw.CommentUseCase().AddCommentToIssue(ctx, id, "actor", text)
					return "comment " + id, err
				})
			},
		},
	}
}
