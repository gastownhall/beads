package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// BatchApplier returns the guarded apply-many surface for this store.
func (s *DoltStore) BatchApplier() (issueops.BatchApplier, error) {
	return NewBatchApplier(s)
}

// NewBatchApplier returns a guarded batch applier backed by store.
func NewBatchApplier(store *DoltStore) (issueops.BatchApplier, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewBatchApplier", Backend: "nil"}
	}
	return &batchApplier{store: store}, nil
}

type batchApplier struct{ store *DoltStore }

var _ issueops.BatchApplier = (*batchApplier)(nil)

// ApplyBatch runs every item in ONE transaction with one commit.
//
// VALIDATION HAPPENS BEFORE THE TRANSACTION, which makes the role's "a refusal
// changes nothing" true of the connection as well as of the rows: an
// unresolvable ref or a guard on a row the request itself rewrites costs no
// database work at all.
//
// The message is composed inside the body because its default names how much
// LANDED, which is not knowable until every item has run — an update that
// matched and an edge that was already there both land nothing.
func (o *batchApplier) ApplyBatch(ctx context.Context, request issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	plan, err := storage.PlanApplyBatch(request)
	if err != nil {
		return issueops.ApplyBatchResult{}, err
	}

	var result issueops.ApplyBatchResult
	err = o.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		attempt, write, err := storageissueops.ApplyBatchInTx(ctx, tx, plan)
		if err != nil {
			return nil, "", err
		}
		result = attempt
		return write.Tables, storageissueops.ApplyBatchCommitMessage(plan, attempt, write), nil
	})
	if err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	return result, nil
}
