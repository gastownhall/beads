package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// CloseResult holds the result of a CloseIssueInTx call.
type CloseResult struct {
	IsWisp        bool
	AlreadyClosed bool
}

// CloseIssueInTx closes an issue within a transaction, setting status to closed
// and recording the close event. Routes to the correct table (issues/wisps)
// automatically. The caller is responsible for Dolt versioning if needed.
func CloseIssueInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string) (*CloseResult, error) {
	return closeIssueInTx(ctx, tx, id, reason, actor, session, true, nil)
}

func CloseIssueWithoutEventInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string) (*CloseResult, error) {
	return closeIssueInTx(ctx, tx, id, reason, actor, session, false, nil)
}

// CloseIssueIfMatchInTx closes an issue iff its current revision equals
// *expectedRevision — whole-row optimistic concurrency for close. A nil
// expectedRevision closes unconditionally, matching CloseIssueInTx. On a revision
// mismatch it returns a *storage.PreconditionFailedError carrying the current
// revision and row; an already-closed issue whose revision still matches is an
// idempotent success (AlreadyClosed).
func CloseIssueIfMatchInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string, expectedRevision *int64) (*CloseResult, error) {
	return closeIssueInTx(ctx, tx, id, reason, actor, session, true, expectedRevision)
}

//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func closeIssueInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string, recordEvent bool, expectedRevision *int64) (*CloseResult, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	var affectedIssues, affectedWisps []string
	var aerr error
	if isWisp {
		affectedIssues, affectedWisps, aerr = AffectedByStatusChangeForWispInTx(ctx, tx, id)
	} else {
		affectedIssues, affectedWisps, aerr = AffectedByStatusChangeInTx(ctx, tx, id)
	}
	if aerr != nil {
		return nil, fmt.Errorf("affected by close for %s: %w", id, aerr)
	}

	now := time.Now().UTC()

	// row_lock is rewritten on close so a concurrent reclaim (which also rewrites
	// row_lock) collides on this cell and is forced to conflict-and-retry rather
	// than silently cell-merging a revert-to-ready over a completed close (see
	// lease.go). lease_expires_at/heartbeat_at are cleared: a closed issue holds
	// no lease.
	//
	// Every close also stamps a fresh opaque revision nonce (B1.2). On a guarded
	// close reroll so the cell always differs from the token being replaced, and
	// add the revision predicate to the WHERE.
	rev := NewRevision()
	if expectedRevision != nil {
		for rev == *expectedRevision {
			rev = NewRevision()
		}
	}
	where := "id = ? AND status != ?"
	args := []interface{}{types.StatusClosed, now, now, reason, session, freshRowLock(), rev, id, types.StatusClosed}
	if expectedRevision != nil {
		where += " AND revision = ?"
		args = append(args, *expectedRevision)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(
		"UPDATE %s SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?, "+
			"lease_expires_at = NULL, heartbeat_at = NULL, row_lock = ?, revision = ? WHERE %s",
		issueTable, where), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to close issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		// Disambiguate not-found / (guarded) precondition miss / already-closed.
		var status string
		var currentRevision int64
		qerr := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT status, revision FROM %s WHERE id = ?`, issueTable), id,
		).Scan(&status, &currentRevision)
		if qerr == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
		}
		if qerr != nil {
			return nil, fmt.Errorf("failed to check issue existence: %w", qerr)
		}
		// A revision mismatch is a genuine precondition failure — the row changed
		// under the caller — even if it happens to already be closed.
		if expectedRevision != nil && currentRevision != *expectedRevision {
			cur, gerr := GetIssueInTx(ctx, tx, id)
			if gerr != nil {
				return nil, gerr
			}
			return nil, &storage.PreconditionFailedError{
				ID:               id,
				ExpectedRevision: *expectedRevision,
				CurrentRevision:  currentRevision,
				CurrentIssue:     cur,
			}
		}
		// Revision matched (or unconditional) but the row was already closed: the
		// desired end state holds, so this is an idempotent success.
		if types.Status(status) == types.StatusClosed {
			return &CloseResult{IsWisp: isWisp, AlreadyClosed: true}, nil
		}
		return nil, fmt.Errorf("failed to close issue: %s", id)
	}

	if recordEvent {
		if err := RecordEventInTable(ctx, tx, eventTable, id, types.EventClosed, actor, reason); err != nil {
			return nil, fmt.Errorf("failed to record event: %w", err)
		}
	}

	if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return nil, fmt.Errorf("recompute is_blocked after close for %s: %w", id, err)
	}

	return &CloseResult{IsWisp: isWisp}, nil
}
