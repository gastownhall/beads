package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// BatchCreator returns the guarded create-many surface for this store.
func (s *DoltStore) BatchCreator() (issueops.BatchCreator, error) {
	return NewBatchCreator(s)
}

// NewBatchCreator returns a guarded batch creator backed by store.
func NewBatchCreator(store *DoltStore) (issueops.BatchCreator, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewBatchCreator", Backend: "nil"}
	}
	return &batchCreator{store: store}, nil
}

type batchCreator struct{ store *DoltStore }

// CreateBatch runs every create in ONE transaction with one commit. The
// message is composed inside the body because its default names how much
// LANDED, which is not knowable until every item has been prepared and routed
// to its plane.
//
// Durability contract: a nil error means the batch's data is durable in the
// branch working set. The Dolt history commit runs after the SQL transaction
// (runIssueOperationTxWithMessage) and may be deferred — if it fails after
// retries the creates still landed and ride the next Dolt commit; the only
// signal is the bd.db.post_tx_commit_dropped counter, since the batch path has
// no verify-by-re-read recovery. A nil return is therefore not a retry signal:
// the creates already applied, and retrying would double-create.
func (o *batchCreator) CreateBatch(ctx context.Context, request issueops.CreateBatchRequest) (issueops.CreateBatchResult, error) {
	if err := storageissueops.ValidateCreateBatchRequest(request); err != nil {
		return issueops.CreateBatchResult{}, err
	}

	var result issueops.CreateBatchResult
	err := o.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		attempt, tables, err := storageissueops.ExecuteCreateBatch(ctx, tx, request)
		if err != nil {
			return nil, "", err
		}
		result = attempt
		return tables, storageissueops.CreateBatchCommitMessage(request, attempt), nil
	})
	if err != nil {
		return issueops.CreateBatchResult{}, err
	}
	return result, nil
}

var _ issueops.BatchCreator = (*batchCreator)(nil)
