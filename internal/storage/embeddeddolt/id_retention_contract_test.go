//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestIDRetentionContract runs the retaining-sweep contract against the
// embedded store. It shares internal/storage/issueops.SweepInTx and the create
// guards with the server-backed store; what this wiring catches is the
// embedded store's own staging — the versioned tombstone table must reach the
// commit it publishes after the sweep's transaction — and its transaction
// surface.
//
// One environment for the whole suite, subtests sequential, for the reasons
// TestSweeperContract gives.
func TestIDRetentionContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "idr")
	ctx := t.Context()
	fixture := newEmbeddedIDRetentionFixture(t, te, "idr")

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
	t.Run("APlainSweepSkipsARetainedType", func(t *testing.T) {
		conformance.RunIDRetentionAPlainSweepSkipsARetainedType(t, ctx, fixture)
	})
	t.Run("ARetainedTypeIsHeldBackBeforeTheProtectionsAndTheLimit", func(t *testing.T) {
		conformance.RunIDRetentionARetainedTypeIsHeldBackBeforeTheProtectionsAndTheLimit(t, ctx, fixture)
	})
	t.Run("ARetainedTypeReadErrorFailsTheSweep", func(t *testing.T) {
		conformance.RunIDRetentionARetainedTypeReadErrorFailsTheSweep(t, ctx, fixture)
	})
	t.Run("RefusesARenameOntoARetiredID", func(t *testing.T) {
		conformance.RunIDRetentionRefusesARenameOntoARetiredID(t, ctx, fixture)
	})
}

func newEmbeddedIDRetentionFixture(t *testing.T, te *testEnv, prefix string) conformance.IDRetentionFixture {
	t.Helper()
	sweep := newEmbeddedSweeperFixture(t, te, prefix)
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	applier, err := te.store.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	creator, err := te.store.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	workspaceConfig, err := te.store.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	return conformance.IDRetentionFixture{
		IssuePrefix:     prefix,
		Sweep:           sweep,
		Lifecycle:       lifecycle,
		Reader:          reader,
		BatchApplier:    applier,
		BatchCreator:    creator,
		Importer:        conformance.IDRetentionUpsertImporter(te.store),
		UpsertCreate:    te.store.CreateIssue,
		WorkspaceConfig: workspaceConfig,
		Rename:          te.store.UpdateIssueID,
		TxCreate: func(ctx context.Context, issue *types.Issue, actor string) error {
			return te.store.RunInTransaction(ctx, "id-retention contract", func(tx storage.Transaction) error {
				return tx.CreateIssue(ctx, issue, actor)
			})
		},
		CommittedRetired: func(ctx context.Context, id string) (bool, error) {
			var count int
			err := sweep.QueryScalar(ctx, "SELECT COUNT(*) FROM id_tombstones AS OF 'HEAD' WHERE id = ?", []any{id}, &count)
			return count > 0, err
		},
	}
}
