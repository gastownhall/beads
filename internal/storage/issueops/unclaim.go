package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fenceMismatch returns the ownership-fence refusal when expectedFence is set
// and does not match the row's current claim_fence, and nil otherwise. Both
// release forms check the fence twice — once up front against the read row, and
// once more when disambiguating a 0-row UPDATE — so all four checks route
// through here and the wording (mirroring the ErrStatusMismatch shape) cannot
// drift between them.
func fenceMismatch(id string, current int64, expectedFence *int64) error {
	if expectedFence == nil || current == *expectedFence {
		return nil
	}
	return fmt.Errorf("%w: %s has fence %d, expected %d", storage.ErrFenceMismatch, id, current, *expectedFence)
}

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
// A non-nil expectedFence pins the release to one ownership generation: the
// issue's current claim_fence must still equal *expectedFence or the release
// refuses with storage.ErrFenceMismatch, leaving the row untouched. It is an
// ADDITIONAL conjunct, never a substitute: the ownership check above runs
// first and unchanged, so a satisfied fence does not authorize a cross-actor
// release (a guard establishes freshness, not authority), and force — which
// waives ownership — does not waive a supplied fence.
//
// Only works on issues that have an assignee and status is "open" or
// "in_progress". Returns error if:
//   - Issue is closed (cannot unclaim closed issues)
//   - Issue has no assignee (nothing to unclaim)
//   - Issue is claimed by a different actor and force is false (ErrNotOwner)
//   - expectedFence is non-nil and the current fence differs (ErrFenceMismatch)
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueInTx(ctx context.Context, tx DBTX, id string, actor string, force bool, expectedFence *int64) error {
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

	// Ownership fence conjunct. Checked AFTER the owner check so a stale
	// releaser is told it does not own the issue rather than which generation
	// it missed, and applied even under force.
	if err := fenceMismatch(id, oldIssue.ClaimFence, expectedFence); err != nil {
		return err
	}

	now := time.Now().UTC()

	// Atomic UPDATE: clear assignee, reset status to open, clear started_at,
	// and rewrite row_lock. The predicate re-checks ownership (unless forced)
	// and the fence (when supplied) so a claim that changed hands between the
	// read above and this write is not clobbered. row_lock forces a racing
	// reclaim/close on the same row to conflict rather than silently merge (see
	// lease.go invariant).
	ownerPredicate := "AND assignee = ?"
	args := []interface{}{now, freshRowLock(), id, actor}
	if force {
		// Force still requires a current assignee, but from anyone.
		ownerPredicate = "AND assignee != ''"
		args = []interface{}{now, freshRowLock(), id}
	}
	fencePredicate := ""
	if expectedFence != nil {
		fencePredicate = "AND claim_fence = ?"
		args = append(args, *expectedFence)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = 'open', updated_at = ?,
		    started_at = NULL, claim_fence = claim_fence + 1, row_lock = ?
		WHERE id = ? AND status IN ('open', 'in_progress') %s %s
	`, issueTable, ownerPredicate, fencePredicate), args...)
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
		if err := fenceMismatch(id, current.ClaimFence, expectedFence); err != nil {
			return err
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
// A non-nil expectedFence adds the ownership-fence conjunct: both the assignee
// and the fence must match, and a fence-only miss is reported distinctly as
// storage.ErrFenceMismatch so a caller can tell "someone else holds it" from
// "the same holder, but a later generation of the claim".
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueIfAssigneeInTx(ctx context.Context, tx DBTX, id string, actor string, expectedAssignee string, expectedFence *int64) error {
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
	if err := fenceMismatch(id, oldIssue.ClaimFence, expectedFence); err != nil {
		return err
	}

	now := time.Now().UTC()

	// Atomic UPDATE pinned to the expected assignee (CAS) and, when supplied,
	// the expected fence, applying the same transition as UnclaimIssueInTx:
	// clear assignee, reset status to open, clear started_at, and rewrite
	// row_lock so a racing reclaim/close on the same row conflicts rather than
	// silently merging (see lease.go invariant).
	args := []interface{}{now, freshRowLock(), id, expectedAssignee}
	fencePredicate := ""
	if expectedFence != nil {
		fencePredicate = "AND claim_fence = ?"
		args = append(args, *expectedFence)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = 'open', updated_at = ?,
		    started_at = NULL, claim_fence = claim_fence + 1, row_lock = ?
		WHERE id = ? AND status IN ('open', 'in_progress') AND assignee = ? %s
	`, issueTable, fencePredicate), args...)
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
		// mismatched holder is the CAS verdict (ErrAssigneeMismatch), a
		// mismatched fence is the fence verdict (ErrFenceMismatch — same holder,
		// later ownership generation), otherwise the status is no longer
		// releasable.
		current, gerr := GetIssueInTx(ctx, tx, id)
		if gerr != nil {
			return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
		}
		if current.Assignee != expectedAssignee {
			return fmt.Errorf("%w: %s is held by %q, expected %q", storage.ErrAssigneeMismatch, id, current.Assignee, expectedAssignee)
		}
		if err := fenceMismatch(id, current.ClaimFence, expectedFence); err != nil {
			return err
		}
		return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
	}

	return finishUnclaimInTx(ctx, tx, eventTable, id, actor, oldIssue)
}
