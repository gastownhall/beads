//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// BatchGetter returns the guarded batch-read surface for this store.
func (s *EmbeddedDoltStore) BatchGetter() (issueops.BatchGetter, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "BatchGetter", Backend: "nil"}
	}
	return &batchGetter{store: s}, nil
}

// batchGetter answers a GetMany from one connection's transaction.
//
// It is a sibling of the server-backed store's body rather than a shared
// package for the reason that one gives: the probe and the hydration need a
// TRANSACTION, which storage.DoltStorage does not publish, so the sharing
// happens below both of them at issueops.ExecuteGetMany. The two stores differ
// here only in how they reach a transaction.
type batchGetter struct{ store *EmbeddedDoltStore }

var _ issueops.BatchGetter = (*batchGetter)(nil)

func (g *batchGetter) GetMany(ctx context.Context, request issueops.GetManyRequest) (issueops.GetManyResult, error) {
	var result issueops.GetManyResult
	err := g.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.ExecuteGetMany(ctx, tx, request)
		return err
	})
	if err != nil {
		return issueops.GetManyResult{}, err
	}
	return result, nil
}
