package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestIDRetentionContract runs the retaining-sweep contract against the
// server-backed store: its sweep reaches internal/storage/issueops.SweepInTx
// and stages the versioned tombstone table in the sweep's own commit, and
// every create door here — Lifecycle, the batch roles, the store's upsert
// CreateIssue and a transaction's CreateIssue — meets the shared retired-id
// guard.
//
// One store and one copy-on-write branch for the whole suite, subtests
// sequential, for the history-delta reason TestSweeperContract gives.
func TestIDRetentionContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltIDRetentionFixture(t, "idr")
	defer cleanup()

	t.Run("APlainSweepLeavesTheIDReusable", func(t *testing.T) {
		conformance.RunIDRetentionAPlainSweepLeavesTheIDReusable(t, ctx, fixture)
	})
	t.Run("ARetainingSweepRetiresEverySweptID", func(t *testing.T) {
		conformance.RunIDRetentionARetainingSweepRetiresEverySweptID(t, ctx, fixture)
	})
	t.Run("RefusesARecreateThroughCreate", func(t *testing.T) {
		conformance.RunIDRetentionRefusesARecreateThroughCreate(t, ctx, fixture)
	})
	t.Run("RefusesARecreateOnTheOtherPlane", func(t *testing.T) {
		conformance.RunIDRetentionRefusesARecreateOnTheOtherPlane(t, ctx, fixture)
	})
	t.Run("RefusesThroughABatchApply", func(t *testing.T) {
		conformance.RunIDRetentionRefusesThroughABatchApply(t, ctx, fixture)
	})
	t.Run("RefusesThroughABatchCreate", func(t *testing.T) {
		conformance.RunIDRetentionRefusesThroughABatchCreate(t, ctx, fixture)
	})
	t.Run("RefusesThroughTheStorageSeam", func(t *testing.T) {
		conformance.RunIDRetentionRefusesThroughTheStorageSeam(t, ctx, fixture)
	})
	t.Run("RefusesInsideATransaction", func(t *testing.T) {
		conformance.RunIDRetentionRefusesInsideATransaction(t, ctx, fixture)
	})
	t.Run("ImportSkipsAndReportsARetiredRow", func(t *testing.T) {
		conformance.RunIDRetentionImportSkipsAndReportsARetiredRow(t, ctx, fixture)
	})
	t.Run("NeverMintsARetiredID", func(t *testing.T) {
		conformance.RunIDRetentionNeverMintsARetiredID(t, ctx, fixture)
	})
	t.Run("NeverMintsARetiredChildID", func(t *testing.T) {
		conformance.RunIDRetentionNeverMintsARetiredChildID(t, ctx, fixture)
	})
	t.Run("DryRunRetiresNothing", func(t *testing.T) {
		conformance.RunIDRetentionDryRunRetiresNothing(t, ctx, fixture)
	})
	t.Run("RetiresOnlyWhatItSwept", func(t *testing.T) {
		conformance.RunIDRetentionRetiresOnlyWhatItSwept(t, ctx, fixture)
	})
	t.Run("ARetainingDurableSweepRecordsExactlyOneHistoryEntry", func(t *testing.T) {
		conformance.RunIDRetentionARetainingDurableSweepRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("RefusesANoHistoryRecreateThroughEveryDoor", func(t *testing.T) {
		conformance.RunIDRetentionRefusesANoHistoryRecreateThroughEveryDoor(t, ctx, fixture)
	})
	t.Run("AWispsPlaneRetainingSweepRetiresItsRows", func(t *testing.T) {
		conformance.RunIDRetentionAWispsPlaneRetainingSweepRetiresItsRows(t, ctx, fixture)
	})
	t.Run("RefusesARenameOntoARetiredID", func(t *testing.T) {
		conformance.RunIDRetentionRefusesARenameOntoARetiredID(t, ctx, fixture)
	})
}

// TestIDRetentionContractRaces runs the three race cases on a store of their
// own with several connections (setupConcurrentTestStore). The branch store
// TestIDRetentionContract shares has ONE connection — DOLT_CHECKOUT is
// session-level — so on it the creates and the sweep would run one after
// another and race nothing.
func TestIDRetentionContractRaces(t *testing.T) {
	store, storeCleanup := setupConcurrentTestStore(t)
	fixture, ctx, cleanup := newDoltIDRetentionFixtureFor(t, store, storeCleanup, "idrr")
	defer cleanup()
	if err := store.SetConfig(ctx, "issue_prefix", "idrr"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	t.Run("ARacingRecreateNeverLands", func(t *testing.T) {
		conformance.RunIDRetentionARacingRecreateNeverLands(t, ctx, fixture)
	})
	t.Run("ARacingUpsertNeverResurrects", func(t *testing.T) {
		conformance.RunIDRetentionARacingUpsertNeverResurrects(t, ctx, fixture)
	})
	t.Run("ARacingImportNeverResurrects", func(t *testing.T) {
		conformance.RunIDRetentionARacingImportNeverResurrects(t, ctx, fixture)
	})
}

// newDoltIDRetentionFixture composes the sweeper fixture with this store's
// create doors.
func newDoltIDRetentionFixture(t *testing.T, prefix string) (conformance.IDRetentionFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	return newDoltIDRetentionFixtureFor(t, store, storeCleanup, prefix)
}

// newDoltIDRetentionFixtureFor is newDoltIDRetentionFixture over a store the
// caller set up, which storeCleanup tears down.
func newDoltIDRetentionFixtureFor(t *testing.T, store *DoltStore, storeCleanup func(), prefix string) (conformance.IDRetentionFixture, context.Context, func()) {
	t.Helper()
	sweep, store, ctx, cleanup := newDoltSweeperFixtureFor(t, store, storeCleanup, prefix)
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		cleanup()
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	reader, err := store.IssueReader()
	if err != nil {
		cleanup()
		t.Fatalf("IssueReader(): %v", err)
	}
	applier, err := store.BatchApplier()
	if err != nil {
		cleanup()
		t.Fatalf("BatchApplier(): %v", err)
	}
	creator, err := store.BatchCreator()
	if err != nil {
		cleanup()
		t.Fatalf("BatchCreator(): %v", err)
	}
	return conformance.IDRetentionFixture{
		IssuePrefix:  prefix,
		Sweep:        sweep,
		Lifecycle:    lifecycle,
		Reader:       reader,
		BatchApplier: applier,
		BatchCreator: creator,
		Importer:     conformance.IDRetentionUpsertImporter(store),
		UpsertCreate: store.CreateIssue,
		Rename:       store.UpdateIssueID,
		TxCreate: func(ctx context.Context, issue *types.Issue, actor string) error {
			return store.RunInTransaction(ctx, "id-retention contract", func(tx storage.Transaction) error {
				return tx.CreateIssue(ctx, issue, actor)
			})
		},
		CommittedRetired: func(ctx context.Context, id string) (bool, error) {
			var count int
			err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM id_tombstones AS OF 'HEAD' WHERE id = ?", id).Scan(&count)
			return count > 0, err
		},
	}, ctx, cleanup
}
