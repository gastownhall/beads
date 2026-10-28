package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestIDRetentionContract runs the retaining-sweep contract against the
// unit-of-work provider, the one body that does not run
// internal/storage/issueops.SweepInTx: its sweep retires through
// domain.DeleteIssuesParams.Retire, its guarded creates meet the shared
// EnsureIssueIDAvailableInTx, and its upsert create (the transaction door
// below) meets RefuseRetiredIDInTx inside domain/db's Insert.
//
// One provider for the whole suite and no t.Parallel, for the reason
// TestSweeperContract gives.
func TestIDRetentionContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWIDRetentionFixture(t, ctx, "idr")

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
	t.Run("ARacingRecreateNeverLands", func(t *testing.T) {
		conformance.RunIDRetentionARacingRecreateNeverLands(t, ctx, fixture)
	})
	t.Run("ARacingUpsertNeverResurrects", func(t *testing.T) {
		conformance.RunIDRetentionARacingUpsertNeverResurrects(t, ctx, fixture)
	})
	t.Run("ARacingImportNeverResurrects", func(t *testing.T) {
		conformance.RunIDRetentionARacingImportNeverResurrects(t, ctx, fixture)
	})
	t.Run("RefusesANoHistoryRecreateThroughEveryDoor", func(t *testing.T) {
		conformance.RunIDRetentionRefusesANoHistoryRecreateThroughEveryDoor(t, ctx, fixture)
	})
	t.Run("AWispsPlaneRetainingSweepRetiresItsRows", func(t *testing.T) {
		conformance.RunIDRetentionAWispsPlaneRetainingSweepRetiresItsRows(t, ctx, fixture)
	})
}

func newUOWIDRetentionFixture(t *testing.T, ctx context.Context, prefix string) conformance.IDRetentionFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	sweep := newUOWSweeperFixtureOnProvider(t, provider, prefix)
	lifecycle, err := provider.(IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	reader, err := provider.(IssueReaderSource).IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	applier, err := provider.(BatchApplierSource).BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	creator, err := provider.(BatchCreatorSource).BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	importer, err := provider.(ImporterSource).Importer()
	if err != nil {
		t.Fatalf("Importer(): %v", err)
	}
	// The domain-level create WITHOUT CreateOnly is this backend's upsert
	// door (the one domain/db's Insert serves with ON DUPLICATE KEY UPDATE),
	// and the unit of work it runs in IS its transaction surface, so it serves
	// both the storage-seam and the in-transaction cases.
	upsert := func(ctx context.Context, issue *types.Issue, actor string) error {
		return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			params := domain.CreateIssueParams{Issue: issue, ExplicitID: issue.ID}
			var err error
			if issue.Ephemeral || issue.NoHistory {
				_, err = uw.IssueUseCase().CreateWisp(ctx, params, actor)
			} else {
				_, err = uw.IssueUseCase().CreateIssue(ctx, params, actor)
			}
			return "id-retention upsert create " + issue.ID, err
		})
	}
	return conformance.IDRetentionFixture{
		IssuePrefix:  prefix,
		Sweep:        sweep,
		Lifecycle:    lifecycle,
		Reader:       reader,
		BatchApplier: applier,
		BatchCreator: creator,
		Importer:     importer,
		UpsertCreate: upsert,
		TxCreate:     upsert,
		CommittedRetired: func(ctx context.Context, id string) (bool, error) {
			var count int
			err := sweep.QueryScalar(ctx, "SELECT COUNT(*) FROM id_tombstones AS OF 'HEAD' WHERE id = ?", []any{id}, &count)
			return count > 0, err
		},
	}
}
