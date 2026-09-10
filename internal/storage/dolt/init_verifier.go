package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// InitVerifier returns the guarded identity-read surface for this store.
func (s *DoltStore) InitVerifier() (issueops.InitVerifier, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "InitVerifier", Backend: "nil"}
	}
	return &initVerifier{store: s}, nil
}

// initVerifier reads the substrate's identity inside one READ transaction.
//
// It takes a transaction it could technically do without, because the pair is
// compared as a pair: a prefix read before a concurrent bootstrap beside a
// project id read after one is a torn answer that looks exactly like the
// cross-project mismatch the comparison exists to find.
type initVerifier struct{ store *DoltStore }

var _ issueops.InitVerifier = (*initVerifier)(nil)

func (v *initVerifier) VerifyIdentity(ctx context.Context, _ issueops.VerifyIdentityRequest) (issueops.VerifyIdentityResult, error) {
	var result issueops.VerifyIdentityResult
	if err := v.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.VerifyIdentityInTx(ctx, tx)
		return err
	}); err != nil {
		return issueops.VerifyIdentityResult{}, err
	}
	return result, nil
}
