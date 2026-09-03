//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// InitVerifier returns the guarded identity-read surface for this store.
func (s *EmbeddedDoltStore) InitVerifier() (issueops.InitVerifier, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "InitVerifier", Backend: "nil"}
	}
	return &initVerifier{store: s}, nil
}

// initVerifier reads the substrate's identity inside one connection's
// transaction, so the pair the caller compares is one snapshot rather than two
// reads a concurrent bootstrap can land between.
type initVerifier struct{ store *EmbeddedDoltStore }

var _ issueops.InitVerifier = (*initVerifier)(nil)

func (v *initVerifier) VerifyIdentity(ctx context.Context, _ issueops.VerifyIdentityRequest) (issueops.VerifyIdentityResult, error) {
	var result issueops.VerifyIdentityResult
	// commit=false: this role writes nothing, and a committing connection would
	// make a read look like a mutation to everything watching the store.
	if err := v.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.VerifyIdentityInTx(ctx, tx)
		return err
	}); err != nil {
		return issueops.VerifyIdentityResult{}, err
	}
	return result, nil
}
