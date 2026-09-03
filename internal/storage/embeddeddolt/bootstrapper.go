//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// Bootstrapper returns the guarded identity-seeding surface for this store.
func (s *EmbeddedDoltStore) Bootstrapper() (issueops.Bootstrapper, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Bootstrapper", Backend: "nil"}
	}
	return &bootstrapper{store: s}, nil
}

// bootstrapper seeds a substrate's identity inside one connection's
// transaction. It is a sibling of the server-backed store's body rather than a
// shared package because the work needs a TRANSACTION, which
// storage.DoltStorage does not publish; the sharing happens below both of them
// at issueops.BootstrapInTx.
type bootstrapper struct{ store *EmbeddedDoltStore }

var _ issueops.Bootstrapper = (*bootstrapper)(nil)

// Bootstrap seeds the request's identity. Validation happens before the
// connection is opened, so a refused request costs no database work.
func (b *bootstrapper) Bootstrap(ctx context.Context, req issueops.BootstrapRequest) (issueops.BootstrapResult, error) {
	req, err := workapi.ValidateBootstrapRequest(req)
	if err != nil {
		return issueops.BootstrapResult{}, err
	}
	var result issueops.BootstrapResult
	if err := b.store.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.BootstrapInTx(ctx, tx, req)
		return err
	}); err != nil {
		return issueops.BootstrapResult{}, err
	}
	return result, nil
}
