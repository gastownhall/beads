package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

// TestStateHasherContract runs the StateHasher contract against the
// server-backed store, the leg auto-export's working-set change detection was
// first written for (wy-4ope). It shares the contract with the embedded leg.
func TestStateHasherContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	storagecontract.RunStateHasherContract(t, ctx, storagecontract.StateHasherFixture{
		Prefix: "sh",
		Store:  store,
		// CreateIssue Dolt-commits every durable issue, so write the same row
		// through its SQL transaction alone: the working-set write a server
		// session leaves when its Dolt commit is deferred or skipped.
		CreateIssue: func(ctx context.Context, id, title string) error {
			return store.withRetryTx(ctx, func(tx *sql.Tx) error {
				bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
				if err != nil {
					return err
				}
				_, err = issueops.CreateIssueInTxWithResult(ctx, tx, bc, &types.Issue{
					ID:        id,
					Title:     title,
					Status:    types.StatusOpen,
					Priority:  2,
					IssueType: types.TypeTask,
				}, "tester")
				return err
			})
		},
		ReadIssue: func(ctx context.Context, id string) error {
			_, err := store.GetIssue(ctx, id)
			return err
		},
		Commit:        store.Commit,
		CurrentCommit: store.GetCurrentCommit,
	})
}
