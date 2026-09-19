//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// BatchCloser returns the guarded close-many surface for this store.
func (s *EmbeddedDoltStore) BatchCloser() (issueops.BatchCloser, error) {
	return NewBatchCloser(s)
}

// NewBatchCloser returns a guarded batch closer backed by store.
func NewBatchCloser(store *EmbeddedDoltStore) (issueops.BatchCloser, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewBatchCloser", Backend: "nil"}
	}
	return &batchCloser{store: store}, nil
}

type batchCloser struct{ store *EmbeddedDoltStore }

// CloseBatch runs every close, and the optional claim, in ONE transaction with
// one commit. The message is composed inside the body because it names what
// LANDED, which is not knowable until the last item has been tried.
func (o *batchCloser) CloseBatch(ctx context.Context, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if err := storageissueops.ValidateCloseBatchRequest(request); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	claimFilter, err := batchClaimFilter(request.ClaimNext)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}

	var result issueops.CloseBatchResult
	err = o.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		attempt, tables, err := storageissueops.ExecuteCloseBatch(ctx, tx, request, claimFilter)
		if err != nil {
			return nil, "", err
		}
		result = attempt
		return tables, storageissueops.CloseBatchCommitMessage(attempt), nil
	})
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	return result, nil
}

// batchClaimFilter translates the request's optional claim into the filter the
// storage layer speaks, through the same builder Reader.Ready and
// ReadyClaimer.ClaimNext run. Nil in, nil out: no claim was asked for.
func batchClaimFilter(request *issueops.ReadyRequest) (*types.WorkFilter, error) {
	if request == nil {
		return nil, nil
	}
	filter, err := workapi.BuildReadyFilter(*request)
	if err != nil {
		return nil, err
	}
	return &filter, nil
}

var _ issueops.BatchCloser = (*batchCloser)(nil)
