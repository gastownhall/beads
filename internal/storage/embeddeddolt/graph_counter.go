//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// GraphCounter returns the guarded edge-count surface for this store.
func (s *EmbeddedDoltStore) GraphCounter() (issueops.GraphCounter, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "GraphCounter", Backend: "nil"}
	}
	return &graphCounter{store: s}, nil
}

// graphCounter answers an edge count from one connection's transaction.
//
// It is a sibling of the server-backed store's body rather than a shared
// package for the reason that one gives: the probe and the tally need a
// TRANSACTION, which storage.DoltStorage does not publish, so the sharing
// happens below both of them at issueops.ExecuteEdgeCount. The two stores
// differ here only in how they reach a transaction.
type graphCounter struct{ store *EmbeddedDoltStore }

var _ issueops.GraphCounter = (*graphCounter)(nil)

func (c *graphCounter) CountEdges(ctx context.Context, req issueops.EdgeCountRequest) (issueops.EdgeCountResult, error) {
	var result issueops.EdgeCountResult
	err := c.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.ExecuteEdgeCount(ctx, tx, req)
		return err
	})
	if err != nil {
		return issueops.EdgeCountResult{}, err
	}
	return result, nil
}
