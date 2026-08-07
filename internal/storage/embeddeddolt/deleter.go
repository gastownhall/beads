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

// Deleter returns the named-row erasure surface for this store.
func (s *EmbeddedDoltStore) Deleter() (issueops.Deleter, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Deleter", Backend: "nil"}
	}
	return &deleter{store: s}, nil
}

// deleter erases named rows inside one connection's transaction.
//
// It is a sibling of the server-backed store's body rather than a shared
// package for the reason that one gives: the work needs a TRANSACTION, which
// storage.DoltStorage does not publish, so the sharing happens below both of
// them at issueops.DeleteInTx. The two stores differ here only in how they
// reach a transaction and in whether they record a version-control entry —
// this one's commit runs outside the SQL transaction, so it records none.
type deleter struct{ store *EmbeddedDoltStore }

var _ issueops.Deleter = (*deleter)(nil)

// Delete erases the request's ids. Validation and normalization happen before
// the connection is opened, so a malformed request costs no database work.
func (s *deleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	if err := workapi.ValidateDeleteRequest(req); err != nil {
		return issueops.DeleteResult{}, err
	}
	req.IDs = workapi.NormalizeDeleteIDs(req.IDs)

	var result issueops.DeleteResult
	// commit = !DryRun: a preview writes nothing, so it must not take the
	// committing path — the same distinction Sweep draws here.
	if err := s.store.withConn(ctx, !req.DryRun, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.DeleteInTx(ctx, tx, req)
		return err
	}); err != nil {
		return issueops.DeleteResult{}, err
	}
	return result, nil
}
