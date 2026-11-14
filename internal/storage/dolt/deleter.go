package dolt

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
func (s *DoltStore) Deleter() (issueops.Deleter, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Deleter", Backend: "nil"}
	}
	return &deleter{store: s}, nil
}

// deleter erases named rows inside ONE transaction.
//
// There is no shared constructor package for this role: the work is an
// existence probe, a guard, a delete and a text rewrite that must all see one
// snapshot, and a transaction is not reachable through storage.DoltStorage. The
// sharing happens one level down instead — this body and the embedded store's
// are a few lines each around issueops.DeleteInTx — so two wrappers over one
// body is still ONE vote.
type deleter struct{ store *DoltStore }

var _ issueops.Deleter = (*deleter)(nil)

// Delete erases the request's ids.
//
// VALIDATION AND NORMALIZATION HAPPEN BEFORE THE TRANSACTION, so a malformed
// request costs no database work and the body below sees a deduplicated,
// trimmed id list. The refusals that need the graph — the missing id and the
// dependents guard — happen inside it, because they are reads.
//
// A DRY RUN TAKES A READ TRANSACTION. It writes nothing by construction, so
// giving it a write transaction and an empty commit would make a preview look
// like a mutation to everything watching the store.
//
// THE VERSION-CONTROL ENTRY IS ONE PER DELETION, minted by the shared post-tx
// publisher (GH#5752) once the write transaction — and, for a fenced delete,
// the fence holding it — has returned. The role promises exactly one entry in
// the STEADY STATE; a publication failure is dropped by the publisher (the
// deletion is durable and rides the next Dolt commit), so a caller retry
// cannot mint a second entry for one deletion. A deletion confined to the wisp
// tables touches only tables this plane ignores, so DOLT_COMMIT finds nothing
// to commit and records none.
func (s *deleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	if err := workapi.ValidateDeleteRequest(req); err != nil {
		return issueops.DeleteResult{}, err
	}
	req.IDs = workapi.NormalizeDeleteIDs(req.IDs)

	var result issueops.DeleteResult
	var publish bool
	run := func(tx *sql.Tx) error {
		var err error
		result, err = storeops.DeleteInTx(ctx, tx, req)
		return err
	}
	if req.DryRun {
		if err := s.store.withReadTx(ctx, run); err != nil {
			return issueops.DeleteResult{}, err
		}
		return result, nil
	}

	write := func(tx *sql.Tx) error {
		// withRetryTx/withDeleteFence may replay this body on a serialization
		// failure: reset so a rolled-back attempt's result never leaks into
		// the value returned below (the withRetryTx closure contract).
		result = issueops.DeleteResult{}
		publish = false
		if err := run(tx); err != nil {
			return err
		}
		// The publication intent is recorded only when the deletion wrote rows;
		// the shared publisher stages the same fixed list a sweep stages (the
		// neighbor rewrite lands in `issues`, which is already on the list) and
		// appends the versioned-history tables itself, deferring under a
		// deferred-commit context (bd-4wamg) exactly as it does everywhere else.
		publish = result.Deleted > 0
		return nil
	}

	// A guarded (ExpectedVersion-checked) delete on a real Dolt sql-server
	// additionally runs under the per-id delete fence (mc-zndi7.73): without
	// it, two same-token deletes race to identical "row absent" diffs that
	// Dolt's commit-time merge lands with no conflict, so every racer exits
	// 0. Embedded Dolt has no concurrent sql-server sessions to race this
	// way, and Postgres's own serializable isolation already turns an
	// identical concurrent delete into a real conflict, so the fence is
	// scoped to serverMode only. See withDeleteFence's doc for the full
	// design and why one pinned connection, rather than a pool-sourced one,
	// is required.
	if req.ExpectedVersion != nil && s.store.serverMode {
		if err := s.store.withDeleteFence(ctx, req.IDs[0], write); err != nil {
			return issueops.DeleteResult{}, err
		}
		s.publishIntent(ctx, publish, result.Deleted)
		return result, nil
	}

	if err := s.store.withRetryTx(ctx, write); err != nil {
		return issueops.DeleteResult{}, err
	}
	s.publishIntent(ctx, publish, result.Deleted)
	return result, nil
}

// publishIntent mints the trailing Dolt history commit for a deletion that
// reported deleted rows, after the runner (and the delete fence, when one held
// the id) has returned. It is a method only to keep the table list beside its
// one caller; the tables are sweptTables, the same fixed list the sweeper
// stages.
func (s *deleter) publishIntent(ctx context.Context, publish bool, deleted int) {
	if !publish {
		return
	}
	s.store.publishCommittedTables(ctx, sweptTables, fmt.Sprintf("bd: delete %d issue(s)", deleted))
}
