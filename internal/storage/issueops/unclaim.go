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

	guard, hasGuard := GuardFrom(ctx)

	// Validate: cannot unclaim closed issues. Under a guard this is an
	// ownership-state mismatch like any other — the guarded caller keyed its
	// decision on a snapshot that no longer holds — so it maps to the typed
	// conflict (exit 9) instead of a generic error, keeping orchestrator
	// retry logic on one contract for every "state moved" outcome.
	if oldIssue.Status == types.StatusClosed {
		if hasGuard {
			return guardConflictFromIssue(oldIssue, guard)
		}
		return fmt.Errorf("cannot unclaim closed issue %s", id)
	}

	// Validate: must have an assignee to unclaim. Same guarded mapping: an
	// already-released row is the single most common race outcome for the
	// orchestrator release path guards exist to serve.
	if oldIssue.Assignee == "" {
		if hasGuard {
			return guardConflictFromIssue(oldIssue, guard)
		}
		return fmt.Errorf("issue %s is not assigned", id)
	}

	// Authorization (the class-T rule, see guard.go): a satisfied explicit
	// guard authorizes a cross-actor release — the guard is the credential —
	// so the owner pre-check applies only to unguarded, unforced calls.
	if !force && !hasGuard && oldIssue.Assignee != actor {
		return fmt.Errorf("%w: %s is held by %s (use --force to override)",
			storage.ErrNotOwner, id, oldIssue.Assignee)
	}

	now := time.Now().UTC()

	// Atomic UPDATE: clear assignee, reset status to open, clear the lease
	// columns, and rewrite row_lock. The predicate re-checks ownership (owner
	// match, or the supplied guard — force alone still requires a current
	// assignee) so a claim that changed hands between the read above and this
	// write is not clobbered. force never skips a supplied guard. row_lock
	// forces a racing heartbeat/reclaim on the same row to conflict rather
	// than silently merge (see lease.go invariant).
	ownerPredicate := "AND assignee = ?"
	args := []interface{}{now, freshRowLock(), id, actor}
	if force || hasGuard {
		ownerPredicate = "AND assignee != ''"
		args = []interface{}{now, freshRowLock(), id}
	}
	guardClause, guardArgs := guard.whereClause()
	args = append(args, guardArgs...)
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', holder_token = '', status = 'open', updated_at = ?,
		    lease_expires_at = NULL, heartbeat_at = NULL, started_at = NULL,
		    claim_fence = claim_fence + 1, row_lock = ?
		WHERE id = ? AND status IN ('open', 'in_progress') %s%s
	`, issueTable, ownerPredicate, guardClause), args...)
	if err != nil {
		return fmt.Errorf("failed to unclaim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		if hasGuard {
			return GuardPreconditionError(ctx, tx, issueTable, id, guard)
		}
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

	// Record the unclaim event. The audit fields make bypassed checks
	// enumerable: a cross-actor release is visible with the channel that
	// authorized it (guard vs force), feeding the advisory taxonomy the
	// enforcement rollout gates on.
	oldData, _ := json.Marshal(oldIssue)
	newUpdates := map[string]interface{}{
		"assignee": "",
		"status":   "open",
	}
	if actor != oldIssue.Assignee {
		newUpdates["cross_actor"] = true
	}
	if force {
		newUpdates["forced"] = true
	}
	if hasGuard {
		guarded := map[string]interface{}{}
		if guard.Assignee != nil {
			guarded["if_assignee"] = *guard.Assignee
		}
		if guard.Fence != nil {
			guarded["if_fence"] = *guard.Fence
		}
		newUpdates["guarded"] = guarded
	}
	newData, _ := json.Marshal(newUpdates)

	if err := RecordFullEventInTable(ctx, tx, eventTable, id, "unclaimed", actor, string(oldData), string(newData)); err != nil {
		return fmt.Errorf("failed to record unclaim event: %w", err)
	}

	return nil
}
