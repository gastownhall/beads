package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// BlockedRecomputeCommitMsg is the Dolt commit message the FULL is_blocked
// repair lands under. Every storage mode commits the repair under this exact
// message, so a reader of dolt_log cannot tell which mode ran it — which is the
// point: the repair is mode-independent (bd-6dnrw.37) and its history entry
// must be too.
const BlockedRecomputeCommitMsg = "bd: recompute is_blocked (full)"

// BlockedRecomputeStagedTables is the exact staging set of the repair commit.
//
// ONLY `issues`. is_blocked lives on issues and wisps, but the wisp tables are
// dolt_ignore'd and can never be staged, and staging anything wider would sweep
// an unrelated dirty working set into a commit whose whole content is supposed
// to be a derived flag. A fresh map per call because StageAndCommit's parameter
// is a mutable set.
func BlockedRecomputeStagedTables() map[string]bool {
	return map[string]bool{"issues": true}
}

// GuardedRecomputeAllBlockedInTx is the entire in-transaction body of the full
// is_blocked repair, shared verbatim by every mode's wrapper: refuse a dirty
// graph, then recompute every issue and wisp, returning the number of rows
// corrected and the number left as found.
//
// The guard runs as the FIRST statement inside the caller's transaction on
// purpose — it must observe exactly the working set the recompute will read, or
// it is guarding a different database than the one being repaired.
//
// The abstained count (issueops.CountIsBlockedUndecidedInTx) is read LAST and
// in the same transaction, so it describes the rows as the repair left them.
// The order matters for a child: the should-be-blocked union reads a parent's
// stored flag, so a child that holds a dangling wisp edge under a parent this
// repair has just marked is decided by that parent now, and was undecided
// before the repair ran. A failed count fails the repair, because a caller
// told "0 abstained" for a database that was never counted would read a clone
// that cannot see its wisps as one that can.
//
// Idempotent: a consistent database corrects nothing and returns Corrected 0.
// Abstained is a property of the database, not of this run, and repeats.
func GuardedRecomputeAllBlockedInTx(ctx context.Context, tx issueops.DBTX) (storage.BlockedRecomputeCounts, error) {
	if err := issueops.GuardBlockedRecomputeWorkingSet(ctx, tx); err != nil {
		return storage.BlockedRecomputeCounts{}, err
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		return storage.BlockedRecomputeCounts{Corrected: int(changed)}, err
	}
	abstained, err := issueops.CountIsBlockedUndecidedInTx(ctx, tx)
	if err != nil {
		return storage.BlockedRecomputeCounts{Corrected: int(changed)}, fmt.Errorf("recompute is_blocked (count abstained): %w", err)
	}
	return storage.BlockedRecomputeCounts{Corrected: int(changed), Abstained: int(abstained)}, nil
}

// BlockedRecomputeConn is a connection the whole repair can run on end to end:
// it can open the repair's own SQL transaction, and it can issue the
// DOLT_ADD/DOLT_COMMIT that must follow that transaction's commit (Dolt's
// stored procedures cannot run inside an explicit transaction). Both *sql.DB
// and *sql.Conn satisfy it.
type BlockedRecomputeConn interface {
	DBConn
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// RecomputeAllBlockedOnConn runs the whole full is_blocked repair on conn and
// returns the rows it corrected and the rows it left as found: guard +
// recompute + count inside one
// transaction, then — only when something actually changed — stage `issues`
// alone and commit under BlockedRecomputeCommitMsg.
//
// author may be empty, which omits --author and lets the server attribute the
// commit to the connected session. That is the right default for the
// proxied-server plane, whose every other commit (uow.Tx.Commit) is likewise
// unauthored; a store that has a configured committer identity passes it.
//
// The returned counts are meaningful even alongside a non-nil error from the
// staging step: the rows WERE corrected in the working set, only the history
// entry failed, and a caller that reported 0 there would be lying about the
// database it is looking at.
func RecomputeAllBlockedOnConn(ctx context.Context, conn BlockedRecomputeConn, author string) (storage.BlockedRecomputeCounts, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return storage.BlockedRecomputeCounts{}, fmt.Errorf("begin is_blocked recompute: %w", err)
	}
	counts, err := GuardedRecomputeAllBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return storage.BlockedRecomputeCounts{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.BlockedRecomputeCounts{}, fmt.Errorf("commit is_blocked recompute: %w", err)
	}
	if counts.Corrected > 0 {
		if err := StageAndCommit(ctx, conn, BlockedRecomputeStagedTables(), BlockedRecomputeCommitMsg, author); err != nil {
			return counts, err
		}
	}
	return counts, nil
}
