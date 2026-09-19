//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

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
// reach a transaction and in WHEN the version-control entry lands: the server
// writes it inside the write transaction, this one after the SQL commit on a
// second connection.
type deleter struct{ store *EmbeddedDoltStore }

var _ issueops.Deleter = (*deleter)(nil)

// Delete erases the request's ids. Validation and normalization happen before
// the connection is opened, so a malformed request costs no database work.
//
// A DRY RUN TAKES THE NON-COMMITTING PATH: withConn(commit=false) rolls the
// transaction back and no version-control entry is attempted — the same
// distinction Sweep draws here.
//
// THE VERSION-CONTROL ENTRY IS ONE PER DELETION, published through
// runIssueOperationTx like this store's other guarded writes. It was not
// always: the port of `bd delete` onto this role reached its transaction
// through withConn, which mints no Dolt commit, so embedded deletes stopped
// being versioned and were compensated for at the CLI instead. A deletion
// confined to dolt-ignored tables — a wisp with no sync-plane edges — leaves
// nothing pending, so StageAndCommit records none.
//
// THE ENTRY LANDS AFTER THE SQL TRANSACTION COMMITS, on a second connection —
// this store has no way to mint a Dolt commit inside its own transaction. A
// crash in that window leaves the rows deleted and the change sitting
// uncommitted in the working set, for the next flush to carry. "One entry per
// deletion" is therefore a steady-state promise here, where the server-backed
// store's is crash-atomic.
func (s *deleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	if err := workapi.ValidateDeleteRequest(req); err != nil {
		return issueops.DeleteResult{}, err
	}
	req.IDs = workapi.NormalizeDeleteIDs(req.IDs)

	var result issueops.DeleteResult
	run := func(tx *sql.Tx) error {
		var err error
		result, err = storeops.DeleteInTx(ctx, tx, req)
		return err
	}
	if req.DryRun {
		if err := s.store.withConn(ctx, false, run); err != nil {
			return issueops.DeleteResult{}, err
		}
		return result, nil
	}

	if err := s.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		if err := run(tx); err != nil {
			return nil, "", err
		}
		if result.Deleted == 0 {
			return nil, "", nil
		}
		// The same tables a sweep stages; the neighbor rewrite lands in
		// `issues` and `events`, which are already on the list.
		return sweptTables(), fmt.Sprintf("bd: delete %d issue(s)", result.Deleted), nil
	}); err != nil {
		return issueops.DeleteResult{}, err
	}
	return result, nil
}
