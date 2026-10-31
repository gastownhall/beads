package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// Imager returns the raw bead-image surface for this store.
//
// It is not on storage.DoltStorage: callers reach it by type assertion on the
// unwrapped store (storage.UnwrapStore). It reads only, so a hook decorator
// has nothing to add in front of it.
func (s *DoltStore) Imager() (issueops.Imager, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Imager", Backend: "nil"}
	}
	return &imager{store: s}, nil
}

// imager reads bead images inside ONE read transaction. The body is
// issueops.ImageInTx, shared with the embedded store and the unit-of-work
// provider, so all three routes produce the same bytes.
type imager struct{ store *DoltStore }

var _ issueops.Imager = (*imager)(nil)

// Image reads every id's image on one snapshot, so two images joined by an
// edge agree about it.
func (m *imager) Image(ctx context.Context, ids []string) ([]issueops.Image, error) {
	var images []issueops.Image
	if err := m.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		images, err = storeops.ImageInTx(ctx, tx, ids)
		return err
	}); err != nil {
		return nil, err
	}
	return images, nil
}
