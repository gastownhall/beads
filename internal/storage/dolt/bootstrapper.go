package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// Bootstrapper returns the guarded identity-seeding surface for this store.
func (s *DoltStore) Bootstrapper() (issueops.Bootstrapper, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Bootstrapper", Backend: "nil"}
	}
	return &bootstrapper{store: s}, nil
}

// bootstrapper seeds a substrate's identity inside ONE transaction.
//
// There is no shared constructor package for this role, for the reason sweeper
// gives: the work is a read and the write that read QUALIFIES, and a
// transaction is not reachable through storage.DoltStorage. The sharing happens
// one level down instead — this body and the embedded store's are a few lines
// each around issueops.BootstrapInTx — and two wrappers over one body is still
// ONE vote, which the conformance contract says out loud.
type bootstrapper struct{ store *DoltStore }

var _ issueops.Bootstrapper = (*bootstrapper)(nil)

// Bootstrap seeds the request's identity.
//
// VALIDATION HAPPENS BEFORE THE TRANSACTION, so a refused request costs no
// database work — which is what makes issueops.Bootstrapper's "a refusal writes
// nothing" true of the connection as well as of the keys.
//
// NO VERSION-CONTROL ENTRY IS RECORDED HERE, which the role permits and which
// matches what this store's ordinary SetConfig/SetMetadata already do: they
// land in the working set and the front door's own initial commit is what
// records them. Adding a DOLT_COMMIT would give a bootstrap an entry that `bd
// init`'s commit then duplicates.
func (b *bootstrapper) Bootstrap(ctx context.Context, req issueops.BootstrapRequest) (issueops.BootstrapResult, error) {
	req, err := workapi.ValidateBootstrapRequest(req)
	if err != nil {
		return issueops.BootstrapResult{}, err
	}
	var result issueops.BootstrapResult
	if err := b.store.withWriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.BootstrapInTx(ctx, tx, req)
		return err
	}); err != nil {
		return issueops.BootstrapResult{}, err
	}
	return result, nil
}
