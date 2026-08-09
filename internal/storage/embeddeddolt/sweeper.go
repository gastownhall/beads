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
// two differ only in how they reach a transaction and in WHEN the
// version-control entry lands: the server writes it inside the write
// transaction, this one after the SQL commit on a second connection.
type sweeper struct{ store *EmbeddedDoltStore }

var _ issueops.Sweeper = (*sweeper)(nil)

// Sweep clears the request's tier. Validation happens before the connection is
// opened, so a refused request costs no database work.
//
// A DRY RUN TAKES THE NON-COMMITTING PATH: withConn(commit=false) rolls the
// transaction back and no version-control entry is attempted, so a preview
// cannot look like a mutation to anything watching the store.
//
// THE VERSION-CONTROL ENTRY IS ONE PER SWEEP THAT SWEPT, published through
// runIssueOperationTx like this store's other guarded writes. A sweep that
// deleted nothing composes no message and commits nothing, and an ephemeral
// sweep that touched only dolt-ignored tables stages nothing, so
// StageAndCommit finds nothing pending and records none.
//
// THE ENTRY LANDS AFTER THE SQL TRANSACTION COMMITS, on a second connection —
// this store has no way to mint a Dolt commit inside its own transaction. A
// crash in that window leaves the rows swept and the change sitting
// uncommitted in the working set, for the next flush to carry. "One entry per
// sweep" is therefore a steady-state promise here, where the server-backed
// store's is crash-atomic.
func (s *sweeper) Sweep(ctx context.Context, req issueops.SweepRequest) (issueops.SweepResult, error) {
	if err := workapi.ValidateSweepRequest(req); err != nil {
		return issueops.SweepResult{}, err
	}
	var result issueops.SweepResult
	run := func(tx *sql.Tx) error {
		var err error
		result, err = storeops.SweepInTx(ctx, tx, req)
		return err
	}
	if req.DryRun {
		if err := s.store.withConn(ctx, false, run); err != nil {
			return issueops.SweepResult{}, err
		}
		return result, nil
	}

	if err := s.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		if err := run(tx); err != nil {
			return nil, "", err
		}
		if result.Swept == 0 {
			return nil, "", nil
		}
		return sweptTables(), fmt.Sprintf("bd: sweep %d %s bead(s)", result.Swept, req.Tier), nil
	}); err != nil {
		return issueops.SweepResult{}, err
	}
	return result, nil
}

// sweptTables are the versioned tables a sweep stages before its commit. It is
// the server-backed store's list of the same name, derived from the shared
// cascade description rather than hand-listed, so a migration that adds a
// cascade cannot leave rows deleted in the working set and absent from the
// version commit.
//
// It is the set a DELETE stages too, because a sweep IS a delete of a selected
// set. BOTH PLANES ARE ASKED FOR: one call can name rows in either, and
// ChangedTables.Add drops the ephemeral members — leaving the durable set plus
// the sync-plane `dependencies` rows a wisp delete removes explicitly.
func sweptTables() storeops.ChangedTables {
	tables := storeops.ChangedTables{}
	tables.Add(storeops.DeleteCascadeTables(false)...)
	tables.Add(storeops.DeleteCascadeTables(true)...)
	return tables
}
