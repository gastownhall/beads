package uow

import (
	"context"
	"fmt"

	"github.com/cenkalti/backoff/v4"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// recheckBlockedAfterCommit recomputes, on the pinned session and a fresh
// snapshot, the blocked state of the dependents a committed unit of work's
// unblocking writes recorded (gastownhall/beads#6716). The in-transaction
// recompute read that unit of work's start snapshot, so two proxied writes
// racing on the blockers of one dependent each saw the other blocker still in
// place and both committed leaving it blocked and hidden from `bd ready`. A
// recompute over the same ids after commit sees both writes.
//
// It runs no SQL when nothing was recorded. It commits the way the unit of
// work did (commitStatement): a Dolt commit named for the writes when it
// changed an issues row and version commits are not deferred, a plain COMMIT
// otherwise. A serialization loss is retried on a new transaction.
//
// The write it follows is durable, so a failure is reported through
// issueops.ReportBlockedRecheckFailure (the counter and warning the store
// runners use), never returned: an error from Commit tells every caller the
// write did not land. What a failure
// leaves is the stale flag `bd doctor` and `bd recompute-blocked` repair. It
// reports whether the session is clean enough to go back to the pool; false
// means a transaction may still be open on it and it must be poisoned.
func (t *doltServerTx) recheckBlockedAfterCommit(ctx context.Context, pending issueops.BlockedRecheck) bool {
	if pending.Empty() || issueops.InBlockedRecheck(ctx) {
		return true
	}
	ctx, cancel := issueops.BlockedRecheckContext(ctx)
	defer cancel()

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = txRetryInitialInterval
	bo.MaxElapsedTime = DefaultTxRetryMaxElapsed
	clean := true
	err := backoff.Retry(func() error {
		err := t.recheckBlockedOnce(ctx, pending)
		if err == nil || isSerializationError(err) {
			// A serialization failure means the server already rolled the
			// transaction back, so the session is idle for the next attempt.
			return err
		}
		if _, rbErr := t.conn.ExecContext(ctx, "ROLLBACK;"); rbErr != nil {
			clean = false
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(bo, ctx))
	if err != nil {
		issueops.ReportBlockedRecheckFailure(ctx, pending, issueops.BlockedRecheckFailed(err))
	}
	return clean
}

// recheckBlockedOnce is one recheck transaction on the pinned session.
func (t *doltServerTx) recheckBlockedOnce(ctx context.Context, pending issueops.BlockedRecheck) error {
	if _, err := t.conn.ExecContext(ctx, "START TRANSACTION;"); err != nil {
		return fmt.Errorf("begin recheck: %w", err)
	}
	result, err := issueops.RecomputeIsBlockedInTxWithResult(ctx, t.conn, pending.IssueIDs, pending.WispIDs)
	if err != nil {
		return err
	}
	message := ""
	if result.IssueRowsChanged {
		message = pending.CommitMessage()
	}
	stmt, args, err := t.commitStatement(ctx, message)
	if err != nil {
		return err
	}
	_, err = t.conn.ExecContext(ctx, stmt, args...)
	return err
}
