package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

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
//
// finishWrite is the raw site's beginJournalWriteTx cleanup (#6117): once the
// SQL transaction is durable it releases the journal-writer admission lock and
// the pinned session BEFORE the recheck — the recheck is a journaled write of
// its own and must admit on its own turn, never nested inside this write's
// lock or session.
func (s *DoltStore) commitSQLTxAndRecheck(ctx context.Context, op string, tx *sql.Tx, finishWrite func()) error {
	if err := s.commitSQLTx(ctx, op, tx); err != nil {
		return err
	}
	finishWrite()
	s.settleBlockedRecheck(ctx, issueops.TakeBlockedRecheck(tx))
	return nil
}
