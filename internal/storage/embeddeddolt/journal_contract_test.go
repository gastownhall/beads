//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournalContract runs the Journal contract against the embedded store,
// which reaches issueops.ReadEventsPageInTx through its own per-operation
// connection. All three legs share that one body, so this is an ENGINE CHECK
// rather than an independent vote — what it can actually catch here is a
// wrapper that loses the transaction, a withConn arm that commits a read, or a
// backend that stops implementing the seam at all.
//
// One environment for the whole suite, and NO t.Parallel: the journal is
// append-only and workspace-global, two of the cases prune it — one of them to
// nothing — and every case rebaselines off the live head, which only works
// while the subtests run in order (see journal_contract.go's seeding note).
func TestJournalContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "jc")
	ctx := t.Context()
	fixture := newEmbeddedJournalFixture(t, te, "jc")

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
	t.Run("ArrivalStampsAWispCreateWithItsRecordSeq", func(t *testing.T) {
		conformance.RunJournalArrivalStampsAWispCreateWithItsRecordSeq(t, ctx, fixture)
	})
	t.Run("ArrivalOrdersABatchByItem", func(t *testing.T) {
		conformance.RunJournalArrivalOrdersABatchByItem(t, ctx, fixture)
	})
	t.Run("ArrivalIsOnEveryReadPath", func(t *testing.T) {
		conformance.RunJournalArrivalIsOnEveryReadPath(t, ctx, fixture)
	})
	t.Run("ArrivalNeverChanges", func(t *testing.T) {
		conformance.RunJournalArrivalNeverChanges(t, ctx, fixture)
	})
	t.Run("ArrivalOnlyWhileJournaling", func(t *testing.T) {
		conformance.RunJournalArrivalOnlyWhileJournaling(t, ctx, fixture)
	})
	t.Run("ArrivalOnlyOnTheWispPlane", func(t *testing.T) {
		conformance.RunJournalArrivalOnlyOnTheWispPlane(t, ctx, fixture)
	})
	t.Run("ArrivalRecoversCreateOrderAfterTruncation", func(t *testing.T) {
		conformance.RunJournalArrivalRecoversCreateOrderAfterTruncation(t, ctx, fixture)
	})
	t.Run("ArrivalIsNeverReused", func(t *testing.T) {
		conformance.RunJournalArrivalIsNeverReused(t, ctx, fixture)
	})
	t.Run("ArrivalIgnoresSuppliedValues", func(t *testing.T) {
		conformance.RunJournalArrivalIgnoresSuppliedValues(t, ctx, fixture)
	})
	t.Run("ArrivalReimportKeepsTheStamp", func(t *testing.T) {
		conformance.RunJournalArrivalReimportKeepsTheStamp(t, ctx, fixture)
	})
	t.Run("ArrivalFirstCreateRecordStampsAnUnstampedRow", func(t *testing.T) {
		conformance.RunJournalArrivalFirstCreateRecordStampsAnUnstampedRow(t, ctx, fixture)
	})
	t.Run("ArrivalRenameKeepsTheStamp", func(t *testing.T) {
		conformance.RunJournalArrivalRenameKeepsTheStamp(t, ctx, fixture)
	})
	t.Run("EveryMutationKindLandsARow", func(t *testing.T) {
		conformance.RunJournalEveryMutationKindLandsARow(t, ctx, fixture)
	})
}

func newEmbeddedJournalFixture(t *testing.T, te *testEnv, prefix string) conformance.JournalFixture {
	t.Helper()
	store := te.store
	// Through the type assertion `bd serve` makes, never the concrete method
	// set: the journal is not on storage.DoltStorage, so publishing it IS
	// implementing this interface, and a backend that stopped would fail here
	// rather than keep compiling against a struct.
	cursor, ok := any(store).(storage.EventsJournalCursor)
	if !ok {
		t.Fatalf("%T does not implement storage.EventsJournalCursor", store)
	}
	// The store hands out no Importer role: `bd import` runs through its bulk
	// upsert, so the import cases drive that door.
	arrival := conformance.ArrivalHooksFromSource(t, store, func(ctx context.Context, id string) error {
		return store.PromoteFromEphemeral(ctx, id, "actor")
	})
	arrival.Import = conformance.ArrivalUpsertImportHook(store)
	arrival.Rename = conformance.ArrivalRenameHook(store)
	return conformance.JournalFixture{
		IssuePrefix:       prefix,
		Journal:           cursor,
		SetJournalEnabled: store.SetEventsJournalEnabled,
		Prune:             store.PruneEventsJournal,
		Arrival:           arrival,
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
}
