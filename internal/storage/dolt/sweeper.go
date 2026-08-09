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

// Sweeper returns the guarded bulk-clearance surface for this store.
func (s *DoltStore) Sweeper() (issueops.Sweeper, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Sweeper", Backend: "nil"}
	}
	return &sweeper{store: s}, nil
}

// sweeper clears closed rows from one tier inside ONE transaction.
//
// There is no shared constructor package for this role, for the reason
// cycleDetector gives: the work is a candidate query, a recheck, an optional
// reference scan and a delete that must all see one snapshot, and a transaction
// is not reachable through storage.DoltStorage. The sharing happens one level
// down — this body and the embedded store's are a few lines each around
// issueops.SweepInTx — and two wrappers over one body is still ONE vote.
type sweeper struct{ store *DoltStore }

var _ issueops.Sweeper = (*sweeper)(nil)

// Sweep clears the request's tier.
//
// VALIDATION HAPPENS BEFORE THE TRANSACTION, which makes issueops.Sweeper's "a
// refusal changes nothing" true of the connection as well as of the rows.
//
// A DRY RUN TAKES A READ TRANSACTION. It writes nothing by construction, so
// giving it a write transaction and an empty commit would make the preview
// look like a mutation to everything watching the store.
//
// THE VERSION-CONTROL ENTRY IS ONE PER SWEEP, recorded here rather than in the
// shared body because the two backends mint it differently: this one INSIDE
// the write transaction, where the embedded store can only publish after its
// SQL commit, on a second connection. That is why the role promises exactly
// one entry in the STEADY STATE and only this leg makes it atomic with the
// sweep. An ephemeral sweep touches only the wisp tables, which this plane
// ignores, so DOLT_COMMIT finds nothing to commit and records none.
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
		if err := s.store.withReadTx(ctx, run); err != nil {
			return issueops.SweepResult{}, err
		}
		return result, nil
	}

	if err := s.store.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := run(tx); err != nil {
			return err
		}
		if result.Swept == 0 {
			return nil
		}
		for _, table := range sweptTables {
			_ = schema.DrainCall(ctx, tx, "CALL DOLT_ADD(?)", table)
		}
		msg := fmt.Sprintf("bd: sweep %d %s bead(s)", result.Swept, req.Tier)
		if err := schema.DrainCall(ctx, tx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			msg, s.store.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	}); err != nil {
		return issueops.SweepResult{}, err
	}
	return result, nil
}

// sweptTables are the versioned tables a sweep can touch, staged before the
// commit. It is the same list DeleteIssues stages, because a sweep IS a
// delete of a selected set.
var sweptTables = []string{
	"issues", "dependencies", "labels", "comments", "events", "provenance_events",
	"child_counters", "issue_snapshots", "compaction_snapshots",
}
