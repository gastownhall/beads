package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// BatchGetter returns the guarded batch-read surface for this store.
func (s *DoltStore) BatchGetter() (issueops.BatchGetter, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "BatchGetter", Backend: "nil"}
	}
	return &batchGetter{store: s}, nil
}

// batchGetter answers a GetMany from one read transaction.
//
// There is no shared constructor package for this role: the work is an
// existence probe and a hydration that must see ONE snapshot, and a
// transaction is not reachable through storage.DoltStorage. The sharing
// happens one level down at issueops.ExecuteGetMany, which the embedded store
// and the unit-of-work provider both reach as well — so what this leg checks
// is the WRAPPER, and the conformance contract says so at the top.
type batchGetter struct{ store *DoltStore }

var _ issueops.BatchGetter = (*batchGetter)(nil)

// GetMany runs the lookup in ONE read transaction, so an id cannot be reported
// missing by a probe that raced a create a second query then found.
func (g *batchGetter) GetMany(ctx context.Context, request issueops.GetManyRequest) (issueops.GetManyResult, error) {
	var result issueops.GetManyResult
	err := g.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.ExecuteGetMany(ctx, tx, request)
		return err
	})
	if err != nil {
		return issueops.GetManyResult{}, err
	}
	return result, nil
}
