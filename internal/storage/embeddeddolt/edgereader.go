//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// EdgeReader returns the guarded stored-edge surface for this store.
func (s *EmbeddedDoltStore) EdgeReader() (issueops.EdgeReader, error) {
	return newEdgeReader(s)
}

// newEdgeReader returns guarded stored-edge reads backed by store. It is
// unexported for the reason the server-backed sibling gives: the accessor above
// is the door, because that is where each decorator adds its layer.
func newEdgeReader(store *EmbeddedDoltStore) (issueops.EdgeReader, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newEdgeReader", Backend: "nil"}
	}
	return &edgeReader{store: store}, nil
}

type edgeReader struct{ store *EmbeddedDoltStore }

var _ issueops.EdgeReader = (*edgeReader)(nil)

// ReadEdges runs the anchor probe and the edge read on ONE connection, so an
// anchor cannot be reported missing by a probe that raced a create the edge
// read then saw.
func (r *edgeReader) ReadEdges(ctx context.Context, request issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	if err := storageissueops.ValidateEdgeReadRequest(request); err != nil {
		return issueops.EdgeReadResult{}, err
	}
	var result issueops.EdgeReadResult
	err := r.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = storageissueops.ExecuteEdgeRead(ctx, tx, request)
		return err
	})
	if err != nil {
		return issueops.EdgeReadResult{}, err
	}
	return result, nil
}
