//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// Imager returns the raw bead-image surface for this store. Like the
// server-backed store's, it is reached by type assertion on the unwrapped
// store rather than through storage.DoltStorage.
func (s *EmbeddedDoltStore) Imager() (issueops.Imager, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Imager", Backend: "nil"}
	}
	return &imager{store: s}, nil
}

// imager reads bead images inside one connection's transaction, around the
// same issueops.ImageInTx body the server-backed store runs.
type imager struct{ store *EmbeddedDoltStore }

var _ issueops.Imager = (*imager)(nil)

// Image reads every id's image on one snapshot. It takes the non-committing
// path: withConn(commit=false) rolls the transaction back, so a read leaves
// nothing pending and no version-control entry.
func (m *imager) Image(ctx context.Context, ids []string) ([]issueops.Image, error) {
	var images []issueops.Image
	if err := m.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		images, err = storeops.ImageInTx(ctx, tx, ids)
		return err
	}); err != nil {
		return nil, err
	}
	return images, nil
}
