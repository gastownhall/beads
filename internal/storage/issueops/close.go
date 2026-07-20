package issueops

import (
	"context"
	"database/sql"
	"errors"
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
	return closeIssueInTx(ctx, tx, id, reason, actor, session, true)
}

func CloseIssueWithoutEventInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string) (*CloseResult, error) {
	return closeIssueInTx(ctx, tx, id, reason, actor, session, false)
}

// CloseIssueCheckedInTx closes an issue within a transaction, refusing with
// storage.ErrCloseBlocked when it is still blocked (is_blocked=1) unless force
// is set. The guard (IsBlockedInTx) and the close (CloseIssueInTx) share the
// SAME transaction, so no blocker can clear between the check and the close.
func CloseIssueCheckedInTx(ctx context.Context, tx DBTX, id, reason, actor, session string, force bool) (*CloseResult, error) {
	if !force {
		// The blocked guard only has meaning for an open→closed transition. An
		// already-closed row is an idempotent no-op (Unchanged=true per the
		// Storage.CloseIssueChecked contract), so detect that first. A closed
		// row can still carry a stale is_blocked=1 — e.g. after a cross-clone
		// Dolt merge, a state the schema explicitly models (GetStatistics
		// filters `is_blocked = 1 AND status <> 'closed'`). Guarding such a row
		// would refuse the idempotent re-close with ErrCloseBlocked. Only guard
		// rows that are not already closed; CloseIssueInTx below is the sole
		// detector of the already-closed no-op (and matches the Force path,
		// which already reaches Unchanged=true by skipping the guard).
		closed, err := isClosedInTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !closed {
			blocked, blockers, err := IsBlockedInTx(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			if blocked {
				// A purely transitive block (e.g. an active parent-child chain)
				// sets is_blocked=1 without any direct blocks/waits-for/
				// conditional-blocks edge, so blockers is empty — omit the "blocked
				// by" clause rather than render "blocked by []".
				if len(blockers) > 0 {
					return nil, fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, id, blockers)
				}
				return nil, fmt.Errorf("%w: %s", storage.ErrCloseBlocked, id)
			}
		}
	}
	return CloseIssueInTx(ctx, tx, id, reason, actor, session)
}

// isClosedInTx reports whether the issue (or wisp) identified by id is already
// in the closed status. It probes the issues table then the optional wisps
// table, mirroring IsBlockedInTx's table order. A missing row reports false so
// the caller falls through to CloseIssueInTx, which returns ErrNotFound.
//
//nolint:gosec // G201: table is a hardcoded "issues" or "wisps".
func isClosedInTx(ctx context.Context, tx DBTX, id string) (bool, error) {
	for _, table := range []string{"issues", "wisps"} {
		var status string
		err := tx.QueryRowContext(ctx, "SELECT status FROM "+table+" WHERE id = ?", id).Scan(&status)
		if err == nil {
			return types.Status(status) == types.StatusClosed, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if optionalBlockedTable(table) && isTableNotExistError(err) {
			continue
		}
		return false, fmt.Errorf("read status from %s: %w", table, err)
	}
	return false, nil
}

//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func closeIssueInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string, recordEvent bool) (*CloseResult, error) {
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
	// lease.go). The lease row is deleted below: a closed issue holds no lease.
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?,
			row_lock = ?
		WHERE id = ? AND status != ?
	`, issueTable), types.StatusClosed, now, now, reason, session, freshRowLock(), id, types.StatusClosed)
	if err != nil {
		return nil, fmt.Errorf("failed to close issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		var status string
		qerr := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT status FROM %s WHERE id = ?`, issueTable), id,
		).Scan(&status)
		if qerr == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
		}
		if qerr != nil {
			return nil, fmt.Errorf("failed to check issue existence: %w", qerr)
		}
		if types.Status(status) == types.StatusClosed {
			return &CloseResult{IsWisp: isWisp, AlreadyClosed: true}, nil
		}
		return nil, fmt.Errorf("failed to close issue: %s", id)
	}

	// A closed issue holds no lease (no-op for wisps, which are never leased).
	if err := DeleteLeaseInTx(ctx, tx, id); err != nil {
		return nil, err
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
