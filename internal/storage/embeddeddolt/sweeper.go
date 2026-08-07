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

// Sweeper returns the guarded bulk-clearance surface for this store.
func (s *EmbeddedDoltStore) Sweeper() (issueops.Sweeper, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Sweeper", Backend: "nil"}
	}
	return &sweeper{store: s}, nil
}

// sweeper clears closed rows from one tier inside one connection's transaction.
// The work needs a TRANSACTION, which storage.DoltStorage does not publish, so
// the sharing with the server-backed store happens at issueops.SweepInTx. The
// two differ only in how they reach a transaction and in whether they record a
// version-control entry — this one's commit runs outside the SQL transaction,
// so it records none.
type sweeper struct{ store *EmbeddedDoltStore }

var _ issueops.Sweeper = (*sweeper)(nil)

// Sweep clears the request's tier. Validation happens before the connection is
// opened, so a refused request costs no database work.
func (s *sweeper) Sweep(ctx context.Context, req issueops.SweepRequest) (issueops.SweepResult, error) {
	if err := workapi.ValidateSweepRequest(req); err != nil {
		return issueops.SweepResult{}, err
	}
	var result issueops.SweepResult
	// commit = !DryRun: a preview writes nothing, so it must not take the
	// committing path — the same distinction DeleteIssues draws here.
	if err := s.store.withConn(ctx, !req.DryRun, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.SweepInTx(ctx, tx, req)
		return err
	}); err != nil {
		return issueops.SweepResult{}, err
	}
	return result, nil
}
