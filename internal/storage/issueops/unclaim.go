package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// UnclaimIssueInTx atomically releases a claimed issue: it clears the assignee,
// resets status to "open", clears started_at, deletes the issue's lease row
// (see UpsertLeaseInTx) and rewrites row_lock so a concurrent reclaim or close
// on the same row conflicts rather than silently cell-merging (see the
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
func UnclaimIssueInTx(ctx context.Context, tx DBTX, id string, actor string, force bool) error {
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
		return fmt.Errorf("%w: %s is held by %s; coordinate with the holder — pass --force only if their claim is abandoned (crashed agent, expired lease)",
			storage.ErrNotOwner, id, oldIssue.Assignee)
	}

	now := time.Now().UTC()

	// Atomic UPDATE: clear assignee, reset status to open, clear started_at,
	// and rewrite row_lock. The predicate re-checks ownership (unless forced)
	// so a claim that changed hands between the read above and this write is
	// not clobbered. row_lock forces a racing reclaim/close on the same row to
	// conflict rather than silently merge (see lease.go invariant).
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
		    started_at = NULL, row_lock = ?
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
			return fmt.Errorf("%w: %s is held by %s; coordinate with the holder — pass --force only if their claim is abandoned (crashed agent, expired lease)",
				storage.ErrNotOwner, id, current.Assignee)
		}
		return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
	}

	return finishUnclaimInTx(ctx, tx, eventTable, id, actor, oldIssue)
}

// finishUnclaimInTx applies the post-UPDATE half of a release shared by
// UnclaimIssueInTx and UnclaimIssueIfAssigneeInTx: it drops the lease row (a
// no-op when none exists, e.g. a wisp or an open-but-assigned issue that was
// never leased) and records the "unclaimed" event. The row mutation
// (assignee/status/started_at/row_lock) must already have been applied in tx.
func finishUnclaimInTx(ctx context.Context, tx DBTX, eventTable string, id string, actor string, oldIssue *types.Issue) error {
	if err := DeleteLeaseInTx(ctx, tx, id); err != nil {
		return err
	}

	oldData, _ := json.Marshal(oldIssue)
	newData, _ := json.Marshal(map[string]interface{}{
		"assignee": "",
		"status":   "open",
	})
	if err := RecordFullEventInTable(ctx, tx, eventTable, id, "unclaimed", actor, string(oldData), string(newData)); err != nil {
		return fmt.Errorf("failed to record unclaim event: %w", err)
	}
	return nil
}

// UnclaimIssueIfAssigneeInTx atomically releases a claim only while the issue is
// still assigned to expectedAssignee — the compare-and-swap inverse of
// ClaimIssueInTx: a conditional UPDATE ... WHERE id = ? AND assignee = ? with
// RowsAffected as the verdict, so a stale releaser can never clobber a claim
// that has since moved to (or been re-taken by) someone else. On success it
// applies the same transition as UnclaimIssueInTx (assignee cleared, status
// reopened, started_at cleared, lease dropped, row_lock rewritten, "unclaimed"
// event recorded). When the current assignee differs from expectedAssignee —
// including when the issue is no longer assigned at all — it returns
// storage.ErrAssigneeMismatch naming the current holder and leaves the row
// untouched. actor is recorded as the event author.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueIfAssigneeInTx(ctx context.Context, tx DBTX, id string, actor string, expectedAssignee string) error {
	if expectedAssignee == "" {
		return fmt.Errorf("conditional unclaim of %s: expected assignee must not be empty (use UnclaimIssueInTx for an unconditional release)", id)
	}

	// Route to the correct table (issues/wisps) automatically, matching
	// UnclaimIssueInTx.
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for unclaim: %w", err)
	}

	// Validate: cannot unclaim closed issues.
	if oldIssue.Status == types.StatusClosed {
		return fmt.Errorf("cannot unclaim closed issue %s", id)
	}

	// Compare-and-swap precheck: a mismatched holder — including an
	// already-released issue (empty assignee) — is a loud, typed no-op. The read
	// and the UPDATE below run in the same transaction, so this check and the
	// CAS WHERE clause see the same row state.
	if oldIssue.Assignee != expectedAssignee {
		return fmt.Errorf("%w: %s is held by %q, expected %q", storage.ErrAssigneeMismatch, id, oldIssue.Assignee, expectedAssignee)
	}

	now := time.Now().UTC()

	// Atomic UPDATE pinned to the expected assignee (CAS), applying the same
	// transition as UnclaimIssueInTx: clear assignee, reset status to open,
	// clear started_at, and rewrite row_lock so a racing reclaim/close on the
	// same row conflicts rather than silently merging (see lease.go invariant).
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = 'open', updated_at = ?,
		    started_at = NULL, row_lock = ?
		WHERE id = ? AND status IN ('open', 'in_progress') AND assignee = ?
	`, issueTable), now, freshRowLock(), id, expectedAssignee)
	if err != nil {
		return fmt.Errorf("failed to unclaim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// The precheck passed and the read + UPDATE share this transaction, so a
		// 0-row result is not an assignee race (the row cannot change under us
		// mid-tx). Re-read and disambiguate, mirroring UnclaimIssueInTx: a
		// mismatched holder is the CAS verdict (ErrAssigneeMismatch), otherwise
		// the status is no longer releasable.
		current, gerr := GetIssueInTx(ctx, tx, id)
		if gerr != nil {
			return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
		}
		if current.Assignee != expectedAssignee {
			return fmt.Errorf("%w: %s is held by %q, expected %q", storage.ErrAssigneeMismatch, id, current.Assignee, expectedAssignee)
		}
		return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
	}

	return finishUnclaimInTx(ctx, tx, eventTable, id, actor, oldIssue)
}
