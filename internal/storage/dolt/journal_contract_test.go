package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournalContract runs the Journal contract against the server-backed
// store, which reaches issueops.ReadEventsPageInTx through withReadTx. All
// three legs share that one body, so this is an ENGINE CHECK rather than an
// independent vote — but it is the leg whose engine can actually differ: the
// shared sql-server is where a read that escaped its transaction, or a head
// taken from a second one, would have somewhere to go wrong.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch, and they run in order with no t.Parallel: two
// of them prune the journal — one to nothing — and every case rebaselines off
// the live head (see journal_contract.go's seeding note). setupTestStore
// already marks the PARENT parallel.
func TestJournalContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltJournalFixture(t, "jc")
	defer cleanup()

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

func newDoltJournalFixture(t *testing.T, prefix string) (conformance.JournalFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the type assertion `bd serve` makes, never the concrete method
	// set: the journal is not on storage.DoltStorage, so publishing it IS
	// implementing this interface, and a backend that stopped would fail here
	// rather than keep compiling against a struct.
	cursor, ok := any(store).(storage.EventsJournalCursor)
	if !ok {
		cancel()
		storeCleanup()
		t.Fatalf("%T does not implement storage.EventsJournalCursor", store)
	}
	fixture := conformance.JournalFixture{
		IssuePrefix:       prefix,
		Journal:           cursor,
		SetJournalEnabled: store.SetEventsJournalEnabled,
		Prune:             store.PruneEventsJournal,
		Mutations: conformance.JournalMutations{
			Create: func(ctx context.Context, id string) error {
				return store.CreateIssue(ctx, &types.Issue{
					ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
				}, "actor")
			},
			Update: func(ctx context.Context, id string) error {
				return store.UpdateIssue(ctx, id, map[string]any{"title": "renamed " + id}, "actor")
			},
			Close: func(ctx context.Context, id string) error {
				return store.CloseIssue(ctx, id, "done", "actor", "")
			},
			Delete: func(ctx context.Context, id string) error {
				return store.DeleteIssue(ctx, id)
			},
			AddDependency: func(ctx context.Context, from, to string) error {
				return store.AddDependency(ctx, &types.Dependency{
					IssueID: from, DependsOnID: to, Type: types.DepBlocks,
				}, "actor")
			},
			RemoveDependency: func(ctx context.Context, from, to string) error {
				return store.RemoveDependency(ctx, from, to, "actor")
			},
			Comment: func(ctx context.Context, id, text string) error {
				return store.AddComment(ctx, id, "actor", text)
			},
		},
	}
	return fixture, ctx, func() {
		// Journaling is instance-scoped, and this store outlives the fixture in
		// the shared-database harness. Leaving it on would journal every
		// mutation a later test in this package makes.
		store.SetEventsJournalEnabled(false)
		cancel()
		storeCleanup()
	}
}
