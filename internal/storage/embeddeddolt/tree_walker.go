//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// TreeWalker returns the guarded dependency-tree surface for this store.
func (s *EmbeddedDoltStore) TreeWalker() (issueops.TreeWalker, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "TreeWalker", Backend: "nil"}
	}
	return &treeWalker{store: s}, nil
}

// treeWalker answers a dependency-tree walk from one connection's transaction.
//
// It is a sibling of the server-backed store's body rather than a shared package
// for the reason that one gives: the walk needs a TRANSACTION, which
// storage.DoltStorage does not publish, so the sharing happens below both of them
// at issueops.WalkDependencyTreeInTx. The two stores differ here only in how they
// reach a transaction.
type treeWalker struct{ store *EmbeddedDoltStore }

var _ issueops.TreeWalker = (*treeWalker)(nil)

func (t *treeWalker) WalkTree(ctx context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	var result issueops.TreeResult
	err := t.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.WalkDependencyTreeInTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return issueops.TreeResult{}, err
	}
	return result, nil
}
