package dolt

import (
	"context"
	"database/sql"
	"log"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// commitDurableBlockedFlipPostTx publishes, as its own post-tx Dolt commit,
// the durable `issues` rows a wisp-plane dependency write's blocked-state
// maintenance flipped. Wisp writers run on raw transactions with no selective
// doltAddAndCommit of their own (their tables are dolt_ignored), so without
// this the flip sits dirty in the working set with no later dependency commit
// to sweep it (GH#6647). A failure is counted and logged, never returned — the
// write is already durable, and the flip rides the next Dolt commit on the
// branch (runIssueOperationTxWithMessage's trailing-commit contract).
func (s *DoltStore) commitDurableBlockedFlipPostTx(ctx context.Context, commitMsg string) {
	if err := s.doltAddAndCommitPostTx(ctx, []string{"issues"}, commitMsg); err != nil {
		doltMetrics.postTxCommitDropped.Add(ctx, 1)
		log.Printf("dolt: post-tx dolt commit failed for %q (wisp dependency write already committed; is_blocked change rides the next dolt commit): %v",
			commitMsg, err)
	}
}

// settleBlockedRecheck runs the post-commit blocked-state recheck of what a
// committed write recorded (gastownhall/beads#6716) and logs a failure
// instead of returning it: the write is durable, and an error would read as
// "the write did not land" to every caller.
func (s *DoltStore) settleBlockedRecheck(ctx context.Context, pending issueops.BlockedRecheck) {
	logBlockedRecheckFailure(ctx, pending, s.recheckBlockedAfterCommit(ctx, pending))
}

// commitSQLTxAndRecheck is commitSQLTx for a raw write transaction that was
// scoped with issueops.ScopeBlockedRecheckTransaction: once tx has committed
// it settles the dependents tx's unblocking writes recorded. The wisp writers
// and the legacy dependency removal run on raw transactions outside
// withWriteTx, so without this their writes would record into nothing and
// keep the #6716 race.
func (s *DoltStore) commitSQLTxAndRecheck(ctx context.Context, op string, tx *sql.Tx) error {
	if err := s.commitSQLTx(ctx, op, tx); err != nil {
		return err
	}
	s.settleBlockedRecheck(ctx, issueops.TakeBlockedRecheck(tx))
	return nil
}
