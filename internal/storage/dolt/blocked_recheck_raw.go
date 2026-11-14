package dolt

import (
	"context"
	"database/sql"
	"log"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// settleBlockedRecheck runs the post-commit blocked-state recheck of what a
// committed write recorded (gastownhall/beads#6716) and logs a failure
// instead of returning it: the write is durable, and an error would read as
// "the write did not land" to every caller.
func (s *DoltStore) settleBlockedRecheck(ctx context.Context, pending issueops.BlockedRecheck) {
	logBlockedRecheckFailure(ctx, pending, s.recheckBlockedAfterCommit(ctx, pending))
}

// commitWispTxAndRecheck is commitSQLTx for a wisp write's raw transaction
// that was scoped with issueops.ScopeBlockedRecheckTransaction. Once tx has
// committed it publishes the durable rows the write changed
// (commitWispDurable), then settles the dependents tx's unblocking writes
// recorded. The wisp writers run on raw transactions outside withWriteTx, so
// without the recheck their writes would record into nothing and keep the
// #6716 race. The order is the durable writers': the write's own commit first,
// then the commit of its durable rows, then the recheck's.
func (s *DoltStore) commitWispTxAndRecheck(ctx context.Context, op string, tx *sql.Tx, changed wispDurable, commitMsg string) error {
	if err := s.commitSQLTx(ctx, op, tx); err != nil {
		return err
	}
	pending := issueops.TakeBlockedRecheck(tx)
	s.commitWispDurable(ctx, changed, commitMsg)
	s.settleBlockedRecheck(ctx, pending)
	return nil
}

// wispDurable is what a wisp write changed outside the dolt-ignored wisp
// tables: durable rows, which its SQL commit does not version.
type wispDurable struct {
	// issues: the write flipped a durable row's is_blocked — a durable child
	// of a wisp edge's source, a durable dependent of a wisp that closed,
	// reopened or was deleted (an IssueRowsChanged result).
	issues bool
	// dependencies: deleting the wisp removed durable edges that pointed at it
	// (issueops.DeleteWispFromDependenciesInTx).
	dependencies bool
}

// commitWispDurable publishes the durable rows a wisp write changed. The
// wisp tables are dolt-ignored and commit with the SQL transaction, but those
// rows would otherwise sit in the working set (GH#6647). It runs after the
// SQL commit, so the write is already durable: a failed history commit is
// counted and logged like runIssueOperationTxWithMessage's trailing commit,
// never returned, and the rows ride the next Dolt commit. Retrying the write
// does not publish them either, since a change-free retry changes no row.
func (s *DoltStore) commitWispDurable(ctx context.Context, changed wispDurable, commitMsg string) {
	var tables []string
	if changed.dependencies {
		tables = append(tables, "dependencies")
	}
	if changed.issues {
		tables = append(tables, "issues")
	}
	if len(tables) == 0 {
		return
	}
	if err := s.doltAddAndCommitPostTx(ctx, tables, commitMsg); err != nil {
		doltMetrics.postTxCommitDropped.Add(ctx, 1)
		log.Printf("dolt: post-tx dolt commit failed for %q (data already committed; change rides the next dolt commit): %v", commitMsg, err)
	}
}
