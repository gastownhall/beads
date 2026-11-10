package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// GraphCounter returns the guarded edge-count surface for this store.
func (s *DoltStore) GraphCounter() (issueops.GraphCounter, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "GraphCounter", Backend: "nil"}
	}
	return &graphCounter{store: s}, nil
}

// graphCounter answers an edge count from one read transaction.
//
// There is no shared constructor package for this role: the work is an
// existence probe and a tally that must see ONE snapshot, and a transaction is
// not reachable through storage.DoltStorage. The sharing happens one level down
// at issueops.ExecuteEdgeCount, which the embedded store and the unit-of-work
// provider both reach as well — so what this leg checks is the WRAPPER, and the
// conformance contract says so at the top.
type graphCounter struct{ store *DoltStore }

var _ issueops.GraphCounter = (*graphCounter)(nil)

// CountEdges runs the anchor probe and the tally in ONE read transaction, so an
// anchor cannot be reported missing by a probe that raced a create the tally
// then counted.
func (c *graphCounter) CountEdges(ctx context.Context, req issueops.EdgeCountRequest) (issueops.EdgeCountResult, error) {
	var result issueops.EdgeCountResult
	err := c.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.ExecuteEdgeCount(ctx, tx, req)
		return err
	})
	if err != nil {
		return issueops.EdgeCountResult{}, err
	}
	return result, nil
}
