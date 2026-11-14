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
// THE VERSION-CONTROL ENTRY IS ONE PER DELETION, recorded here rather than in
// the shared body, and published AFTER the SQL transaction committed, on a
// connection with no transaction open, exactly as the embedded store does
// (GH#5752: staging inside the open transaction builds the Dolt commit from
// the BEGIN-time root and can write a concurrent writer's rows back). The
// deletion and its history entry are therefore NOT atomic: a publication
// failure after the commit leaves the deletion applied and is counted and
// logged, and a concurrent writer's commit can absorb the rows under its own
// message, which is why the role promises exactly one entry only in the
// STEADY STATE. A deletion confined to the wisp tables touches only tables
// this plane ignores, so the staged-set guard finds nothing and records none.
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
		return run(tx)
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
	runner := s.store.withRetryTx
	if req.ExpectedVersion != nil && s.store.serverMode {
		runner = func(ctx context.Context, fn func(*sql.Tx) error) error {
			return s.store.withDeleteFence(ctx, req.IDs[0], fn)
		}
	}
	if err := runner(ctx, write); err != nil {
		return issueops.DeleteResult{}, err
	}

	// Published only once the runner returned: after the SQL COMMIT and, on
	// the fenced path, after RELEASE_LOCK closed the pinned connection, so a
	// pool of one connection is free for the publisher. The same tables a
	// sweep stages (the neighbor rewrite lands in `issues`, already on the
	// list); stageAndCommit appends the versioned-history tables the rewrite
	// mints into (RewriteDeletedReferencesInTx updates every surviving
	// neighbor through UpdateIssueInTx), and honors a deferred version commit
	// (bd-4wamg).
	if result.Deleted > 0 {
		s.store.publishCommittedOperation(ctx, sweptTables, fmt.Sprintf("bd: delete %d issue(s)", result.Deleted))
	}
	return result, nil
}
