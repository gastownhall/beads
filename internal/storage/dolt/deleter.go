package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
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
// the shared body because the two backends mint it differently: this one
// INSIDE the write transaction, where the embedded store can only publish
// after its SQL commit, on a second connection. That is why the role promises
// exactly one entry in the STEADY STATE and only this leg makes it atomic with
// the deletion. A deletion confined to the wisp tables touches only tables
// this plane ignores, so DOLT_COMMIT finds nothing to commit and records none.
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

	if err := s.store.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := run(tx); err != nil {
			return err
		}
		if result.Deleted == 0 {
			return nil
		}
		// Batch/off auto-commit (bd-4wamg): defer the version commit to an
		// explicit commit point, matching doltAddAndCommitInTx.
		if storeops.VersionCommitDeferred(ctx) {
			return nil
		}
		// The same tables a sweep stages; the neighbor rewrite lands in
		// `issues`, which is already on the list.
		for _, table := range sweptTables {
			_ = schema.DrainCall(ctx, tx, "CALL DOLT_ADD(?)", table)
		}
		msg := fmt.Sprintf("bd: delete %d issue(s)", result.Deleted)
		if err := schema.DrainCall(ctx, tx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			msg, s.store.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	}); err != nil {
		return issueops.DeleteResult{}, err
	}
	return result, nil
}
