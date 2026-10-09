package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// DetailBatchReader returns issue details from one read snapshot.
func (s *DoltStore) DetailBatchReader() (issueops.DetailBatchReader, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "DetailBatchReader", Backend: "nil"}
	}
	return &detailBatchReader{store: s}, nil
}

type detailBatchReader struct{ store *DoltStore }

var _ issueops.DetailBatchReader = (*detailBatchReader)(nil)

func (r *detailBatchReader) GetBatch(ctx context.Context, request issueops.DetailBatchRequest) (issueops.DetailBatchResult, error) {
	if err := issueops.ValidateDetailBatchRequest(request); err != nil {
		return issueops.DetailBatchResult{}, err
	}
	if len(request.IDs) == 0 {
		return issueops.DetailBatchResult{Items: []issueops.DetailBatchItem{}}, nil
	}
	var result issueops.DetailBatchResult
	err := r.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = storageissueops.ExecuteDetailBatch(ctx, tx, request, storageissueops.DetailBatchDirectServer)
		return err
	})
	if err != nil {
		return issueops.DetailBatchResult{}, err
	}
	return result, nil
}
