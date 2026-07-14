package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// UnclaimIssueInTx atomically releases a claimed issue: it clears the assignee,
// resets status to "open", clears the lease columns (lease_expires_at,
// heartbeat_at, started_at) and rewrites row_lock so a concurrent heartbeat or
// reclaim on the same row conflicts rather than silently cell-merging (see the
// row_lock invariant in lease.go). Records an "unclaimed" event.
//
// Ownership: only the current assignee may release its own claim. A mismatched
// actor is rejected with storage.ErrNotOwner rather than a silent no-op, so a
// second agent cannot yank a claim it does not hold. Pass force=true to bypass
// the ownership check (admin/reaper use, threaded from `bd unclaim --force`).
//
// Only works on issues that have an assignee and status is "open" or
// "in_progress". Returns error if:
//   - Issue is closed (cannot unclaim closed issues)
//   - Issue has no assignee (nothing to unclaim)
//   - Issue is claimed by a different actor and force is false (ErrNotOwner)
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueInTx(ctx context.Context, tx *sql.Tx, id string, actor string, force bool) error {
	// Route to the correct table (issues/wisps) automatically, matching
	// ClaimIssueInTx — a wisp claim lives in the wisp tables, so its release
	// must update them too rather than no-op against the permanent issues table.
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for unclaim: %w", err)
	}

	// Validate: cannot unclaim closed issues
	if oldIssue.Status == types.StatusClosed {
		return fmt.Errorf("cannot unclaim closed issue %s", id)
	}

	// Validate: must have an assignee to unclaim
	if oldIssue.Assignee == "" {
		return fmt.Errorf("issue %s is not assigned", id)
	}

	// Validate ownership unless the caller forced the release. Without force, a
	// process may only release its own claim.
	if !force && oldIssue.Assignee != actor {
		return fmt.Errorf("%w: %s is held by %s (use --force to override)",
			storage.ErrNotOwner, id, oldIssue.Assignee)
	}

	now := time.Now().UTC()

	// Atomic UPDATE: clear assignee, reset status to open, clear the lease
	// columns, and rewrite row_lock. The predicate re-checks ownership (unless
	// forced) so a claim that changed hands between the read above and this
	// write is not clobbered. row_lock forces a racing heartbeat/reclaim on the
	// same row to conflict rather than silently merge (see lease.go invariant).
	ownerPredicate := "AND assignee = ?"
	args := []interface{}{now, freshRowLock(), id, actor}
	if force {
		// Force still requires a current assignee, but from anyone.
		ownerPredicate = "AND assignee != ''"
		args = []interface{}{now, freshRowLock(), id}
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = 'open', updated_at = ?,
		    lease_expires_at = NULL, heartbeat_at = NULL, started_at = NULL,
		    row_lock = ?
		WHERE id = ? AND status IN ('open', 'in_progress') %s
	`, issueTable, ownerPredicate), args...)
	if err != nil {
		return fmt.Errorf("failed to unclaim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// The pre-checks passed, so a 0-row result means the row changed
		// underneath us: re-read to disambiguate an ownership change from a
		// status change.
		current, gerr := GetIssueInTx(ctx, tx, id)
		if gerr != nil {
			return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
		}
		if !force && current.Assignee != actor {
			return fmt.Errorf("%w: %s is held by %s (use --force to override)",
				storage.ErrNotOwner, id, current.Assignee)
		}
		return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
	}

	// Record the unclaim event
	oldData, _ := json.Marshal(oldIssue)
	newUpdates := map[string]interface{}{
		"assignee": "",
		"status":   "open",
	}
	newData, _ := json.Marshal(newUpdates)

	if err := RecordFullEventInTable(ctx, tx, eventTable, id, "unclaimed", actor, string(oldData), string(newData)); err != nil {
		return fmt.Errorf("failed to record unclaim event: %w", err)
	}

	return nil
}
