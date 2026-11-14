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
// shared body, and published AFTER the SQL transaction committed, on a
// connection with no transaction open, exactly as the embedded store does
// (GH#5752). The sweep and its history entry are therefore NOT atomic: a
// publication failure leaves the sweep applied and is counted and logged,
// which is why the role promises exactly one entry only in the STEADY STATE.
// A deferred version commit (bd-4wamg) records none here and leaves the rows
// for the explicit commit point. An ephemeral sweep touches only the wisp
// tables, which this plane ignores, so the staged-set guard finds nothing and
// records none.
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

	// withRetryTx, like the deleter: a sweep that loses Dolt's commit-time
	// merge (1213/1205, or the exact 1105 autocommit rollback) never landed,
	// so the whole body replays — candidates re-read, result re-derived. A
	// retaining sweep needs it most: it writes one coordination cell per
	// shard it retires (see issueops.Sweeper on the lock footprint), and a
	// create committing in any of those shards first conflicts it.
	if err := s.store.withRetryTx(ctx, func(tx *sql.Tx) error {
		result = issueops.SweepResult{}
		return run(tx)
	}); err != nil {
		return issueops.SweepResult{}, err
	}
	// Through the same publisher, with the same table list, as the deleter. A
	// sweep mints nothing today — SweepInTx deletes through DeleteIssuesInTx,
	// which rewrites no neighbor, so there is no surviving row to version —
	// but stageAndCommit appends the versioned-history tables for both alike,
	// so the day a sweep gains a surviving-row write it is already staged.
	if result.Swept > 0 {
		s.store.publishCommittedOperation(ctx, sweptTables, fmt.Sprintf("bd: sweep %d %s bead(s)", result.Swept, req.Tier))
	}
	return result, nil
}

// sweptTables are the versioned tables a sweep can touch, staged before the
// commit: the row tables DeleteIssues stages (a sweep IS a delete of a
// selected set) plus the id tombstone tables, which DeleteIssues never writes.
//
// The tombstone tables are on it for the retaining sweep
// (issueops.SweepRequest.RetainIDs): the versioned one must land in the same
// commit as the deletes it records, and staging the clone-local one is a no-op.
var sweptTables = append([]string{
	"issues", "dependencies", "labels", "comments", "events", "provenance_events",
	"child_counters", "issue_snapshots", "compaction_snapshots",
}, storeops.IDTombstoneTables()...)
