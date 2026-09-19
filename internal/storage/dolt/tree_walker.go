package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// TreeWalker returns the guarded dependency-tree surface for this store.
func (s *DoltStore) TreeWalker() (issueops.TreeWalker, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "TreeWalker", Backend: "nil"}
	}
	return &treeWalker{store: s}, nil
}

// treeWalker answers a dependency-tree walk from one read transaction.
//
// There is no shared constructor package for this role: the work is a root
// probe, a recursion of adjacency reads and a hydration per node, all of which
// must see ONE snapshot — and for a `both` walk that covers two directions. A
// transaction is not reachable through storage.DoltStorage, so the sharing
// happens one level down at issueops.WalkDependencyTreeInTx. Two wrappers over
// one body is still ONE vote, and the conformance contract says so.
type treeWalker struct{ store *DoltStore }

var _ issueops.TreeWalker = (*treeWalker)(nil)

func (t *treeWalker) WalkTree(ctx context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	var result issueops.TreeResult
	err := t.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.WalkDependencyTreeInTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return issueops.TreeResult{}, err
	}
	return result, nil
}
