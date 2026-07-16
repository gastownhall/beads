package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ClaimResult holds the result of a ClaimIssueInTx call.
type ClaimResult struct {
	OldIssue *types.Issue
	IsWisp   bool
}

// claimConflictError carries the frozen open-but-assigned conflict message
// unchanged while unwrapping to storage.ErrAlreadyClaimed, so typed consumers
// (exit codes, {code:"already_claimed"} JSON) classify the loss without the
// sentinel's own text prefixing the message.
type claimConflictError struct{ msg string }

func (e *claimConflictError) Error() string { return e.msg }
func (e *claimConflictError) Unwrap() error { return storage.ErrAlreadyClaimed }

// ClaimIssueInTx atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// is currently open and unassigned or already assigned to the same actor.
// Returns storage.ErrAlreadyClaimed if already claimed by a different user.
// Idempotent: re-claiming an in_progress issue by the same actor is a no-op
// success (supports agent retry workflows).
// Routes to the correct table (issues/wisps) automatically.
// The caller is responsible for Dolt versioning (DOLT_ADD/COMMIT) if needed.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func ClaimIssueInTx(ctx context.Context, tx DBTX, id string, actor string) (*ClaimResult, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	// Read old issue inside the transaction for event recording.
	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue for claim: %w", err)
	}

	now := time.Now().UTC()

	// Stamp a lease on the claim: lease_expires_at = now + TTL, heartbeat_at =
	// now, and a fresh row_lock (see lease.go). The lease is what makes a claim
	// recoverable — a worker that dies stops heartbeating and bd reclaim later
	// reverts the issue. row_lock here also forces a concurrent reclaim/heartbeat
	// to conflict rather than silently cell-merge. The claim is an ownership
	// transition, so it also bumps claim_fence (fenceBumpExpr; row_lock pairing
	// satisfied by the lease clause in the same statement).
	leaseClause, leaseArgs, err := ClaimLeaseClause(ctx, tx, now)
	if err != nil {
		return nil, fmt.Errorf("resolve claim lease: %w", err)
	}

	// Record the caller's incarnation token (empty when the caller supplied
	// none — a tokenless/legacy claim). The token distinguishes runtime
	// incarnations of a re-used assignee name; enforcement checks it.
	holderToken := holderTokenFrom(ctx)

	// An issue is claimable from "open" plus any configured custom status whose
	// category is "active" (e.g. a draft->ready->in_progress lifecycle where
	// "ready" should be claimable). WIP/done/frozen customs are excluded so the
	// anti-steal protection from GH-3570 is preserved.
	claimableStatuses, err := ClaimableSourceStatusesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve claimable statuses: %w", err)
	}
	statusPlaceholders, statusArgs := buildSQLInClause(claimableStatuses)

	// Conditional UPDATE: only succeeds while the issue is still claimable.
	// Also set started_at on first transition to in_progress (GH#2796); preserve
	// any existing value so re-claims don't overwrite the original start time.
	var (
		result sql.Result
	)
	if oldIssue.StartedAt == nil {
		args := append([]interface{}{actor, holderToken, now, now}, leaseArgs...)
		args = append(args, id)
		args = append(args, statusArgs...)
		args = append(args, actor)
		result, err = tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, holder_token = ?, status = 'in_progress', updated_at = ?, started_at = ?, %s, %s
			WHERE id = ? AND status IN (%s) AND (assignee = '' OR assignee IS NULL OR assignee = ?)
		`, issueTable, fenceBumpExpr, leaseClause, statusPlaceholders), args...)
	} else {
		args := append([]interface{}{actor, holderToken, now}, leaseArgs...)
		args = append(args, id)
		args = append(args, statusArgs...)
		args = append(args, actor)
		result, err = tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, holder_token = ?, status = 'in_progress', updated_at = ?, %s, %s
			WHERE id = ? AND status IN (%s) AND (assignee = '' OR assignee IS NULL OR assignee = ?)
		`, issueTable, fenceBumpExpr, leaseClause, statusPlaceholders), args...)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to claim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// Query current state inside the same transaction for consistency.
		var currentAssignee sql.NullString
		var currentStatus types.Status
		err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT assignee, status FROM %s WHERE id = ?`, issueTable), id).Scan(&currentAssignee, &currentStatus)
		if err != nil {
			return nil, fmt.Errorf("failed to get current claim state: %w", err)
		}
		assignee := ""
		if currentAssignee.Valid {
			assignee = currentAssignee.String
		}
		// Idempotent: if already claimed in_progress by the same actor, treat as success.
		// This supports agent retry workflows where claim may be called multiple
		// times after transient failures (GH#8).
		if assignee == actor && currentStatus == types.StatusInProgress {
			// A re-claim by the same actor is an ownership re-assertion: refresh
			// the holder token so the CURRENT incarnation owns it. Without this a
			// fresh incarnation of a re-used session name, re-claiming its own
			// in_progress work through the idempotent path, would keep a prior
			// incarnation's token and then classify as a zombie against its own
			// bead — inverting the advisory signal. Only a token-bearing caller
			// refreshes (a tokenless re-claim must not wipe a live token). Not an
			// ownership transition — no fence bump. (A-B3b replaces this with a
			// fenced incarnation_conflict under require mode.)
			if holderToken != "" {
				if _, uerr := tx.ExecContext(ctx, fmt.Sprintf(
					`UPDATE %s SET holder_token = ? WHERE id = ? AND assignee = ? AND status = 'in_progress'`,
					issueTable), holderToken, id, actor); uerr != nil {
					return nil, fmt.Errorf("refresh holder token: %w", uerr)
				}
			}
			return &ClaimResult{OldIssue: oldIssue, IsWisp: isWisp}, nil
		}
		if assignee != "" && assignee != actor {
			if currentStatus == types.StatusOpen {
				// The message text is a frozen contract (downstream substring
				// matchers and this repo's own tests pin "already assigned
				// to"); claimConflictError keeps it byte-identical while
				// wrapping ErrAlreadyClaimed for typed classification.
				return nil, &claimConflictError{msg: fmt.Sprintf(
					"issue already assigned to %q. Use `bd unclaim %s` to release it before re-claiming", assignee, id)}
			}
			return nil, fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, assignee)
		}
		return nil, fmt.Errorf("%w: status %s", storage.ErrNotClaimable, currentStatus)
	}

	// Record the claim event.
	oldData, _ := json.Marshal(oldIssue)
	newUpdates := map[string]interface{}{
		"assignee": actor,
		"status":   "in_progress",
	}
	newData, _ := json.Marshal(newUpdates)

	if err := RecordFullEventInTable(ctx, tx, eventTable, id, "claimed", actor, string(oldData), string(newData)); err != nil {
		return nil, fmt.Errorf("failed to record claim event: %w", err)
	}

	return &ClaimResult{OldIssue: oldIssue, IsWisp: isWisp}, nil
}

// ClaimReadyIssueInTx claims the first currently ready issue matching filter in
// the same transaction that computes readiness. It returns nil when no matching
// ready issue can be claimed.
func ClaimReadyIssueInTx(
	ctx context.Context,
	tx DBTX,
	filter types.WorkFilter,
	actor string,
) (*types.Issue, error) {
	claimFilter := filter
	claimFilter.Status = types.StatusOpen
	claimFilter.Unassigned = true
	claimFilter.Assignee = nil
	claimFilter.Limit = 0

	readyIssues, err := GetReadyWorkInTx(ctx, tx, claimFilter)
	if err != nil {
		return nil, err
	}
	for _, issue := range readyIssues {
		if _, err := ClaimIssueInTx(ctx, tx, issue.ID, actor); err != nil {
			if errors.Is(err, storage.ErrAlreadyClaimed) || errors.Is(err, storage.ErrNotClaimable) {
				continue
			}
			return nil, err
		}
		claimed, err := GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("get claimed issue: %w", err)
		}
		return claimed, nil
	}
	return nil, nil
}

// ClaimableSourceStatusesInTx returns the set of statuses an issue may be
// claimed FROM: the built-in "open" status plus any configured custom status
// whose category is "active" (the same category that surfaces issues in
// bd ready). Custom statuses in the wip/done/frozen categories are intentionally
// excluded so claim retains its anti-steal protection (GH-3570) — an
// in_progress/blocked issue, or a custom alias for one, is never silently
// re-claimable. Unspecified-category customs are also excluded, matching their
// absence from bd ready.
func ClaimableSourceStatusesInTx(ctx context.Context, tx DBTX) ([]string, error) {
	statuses := []string{string(types.StatusOpen)}
	customs, err := ResolveCustomStatusesDetailedInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, s := range customs {
		if s.Category == types.CategoryActive {
			statuses = append(statuses, s.Name)
		}
	}
	return statuses, nil
}
