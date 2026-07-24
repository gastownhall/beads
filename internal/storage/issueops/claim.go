package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ClaimResult holds the result of a ClaimIssueInTx call.
type ClaimResult struct {
	OldIssue *types.Issue
	IsWisp   bool
}

// ClaimIssueInTx atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// is currently open and unassigned, already assigned to the same actor, or
// assigned to a pool alias listed in the claim.pools config (see
// ClaimPoolAliasesInTx).
// Returns storage.ErrAlreadyClaimed if already claimed by a different user.
// Idempotent: re-claiming an in_progress issue by the same actor is a no-op
// success (supports agent retry workflows).
// Routes to the correct table (issues/wisps) automatically.
// The caller is responsible for Dolt versioning (DOLT_ADD/COMMIT) if needed.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func ClaimIssueInTx(ctx context.Context, tx DBTX, id string, actor string) (*ClaimResult, error) {
	// The CAS below writes assignee = actor. actor is user-settable (--actor /
	// BEADS_ACTOR), so bound it against the VARCHAR(255) assignee column up front
	// and return a typed ErrFieldTooLong rather than a raw backend error.
	if err := types.CheckFieldLen("actor", actor); err != nil {
		return nil, err
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	// Read old issue inside the transaction for event recording.
	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue for claim: %w", err)
	}

	now := time.Now().UTC()

	// Rewrite row_lock with the claim (see lease.go): a concurrent reclaim or
	// close on the same row is forced to conflict rather than silently
	// cell-merge. The lease itself is granted separately below, in the
	// ephemeral leases table — claims commit (status/assignee are
	// history-worthy) but lease grants and heartbeats do not (bd-lrgn1).
	rowLockClause, rowLockArgs := RowLockClause()

	// An issue is claimable from "open" plus any configured custom status whose
	// category is "active" (e.g. a draft->ready->in_progress lifecycle where
	// "ready" should be claimable). WIP/done/frozen customs are excluded so the
	// anti-steal protection from GH-3570 is preserved.
	claimableStatuses, err := ClaimableSourceStatusesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve claimable statuses: %w", err)
	}
	statusPlaceholders, statusArgs := buildSQLInClause(claimableStatuses)

	// Pool-aware claim (bd-bguz6): a dispatcher may pre-assign issues to a
	// pool pseudo-assignee (e.g. "fable-crew"). Aliases listed in the
	// claim.pools config are claimable by any actor through the same CAS;
	// issues assigned to a real actor keep their anti-steal protection.
	pools, err := ClaimPoolAliasesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve claim pools: %w", err)
	}
	assigneePredicate := "assignee = '' OR assignee IS NULL OR assignee = ?"
	assigneeArgs := []interface{}{actor}
	if len(pools) > 0 {
		poolPlaceholders, poolArgs := buildSQLInClause(pools)
		assigneePredicate += " OR assignee IN (" + poolPlaceholders + ")"
		assigneeArgs = append(assigneeArgs, poolArgs...)
	}

	// Conditional UPDATE: only succeeds while the issue is still claimable.
	// Also set started_at on first transition to in_progress (GH#2796); preserve
	// any existing value so re-claims don't overwrite the original start time.
	var (
		result sql.Result
	)
	if oldIssue.StartedAt == nil {
		args := append([]interface{}{actor, now, now}, rowLockArgs...)
		args = append(args, id)
		args = append(args, statusArgs...)
		args = append(args, assigneeArgs...)
		result, err = tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, status = 'in_progress', updated_at = ?, started_at = ?, %s
			WHERE id = ? AND status IN (%s) AND (%s)
		`, issueTable, rowLockClause, statusPlaceholders, assigneePredicate), args...)
	} else {
		args := append([]interface{}{actor, now}, rowLockArgs...)
		args = append(args, id)
		args = append(args, statusArgs...)
		args = append(args, assigneeArgs...)
		result, err = tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, status = 'in_progress', updated_at = ?, %s
			WHERE id = ? AND status IN (%s) AND (%s)
		`, issueTable, rowLockClause, statusPlaceholders, assigneePredicate), args...)
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
			return &ClaimResult{OldIssue: oldIssue, IsWisp: isWisp}, nil
		}
		if assignee != "" && assignee != actor {
			// A pool-assigned issue reaches here only when the CAS lost for a
			// non-assignee reason (status changed underneath us): report the
			// status rather than a misleading held-by-someone refusal.
			if slices.Contains(pools, assignee) {
				return nil, fmt.Errorf("%w%s%s", storage.ErrNotClaimable, storage.NotClaimableStatusFragment, currentStatus)
			}
			if currentStatus == types.StatusOpen {
				// Do not name a release command here — not `bd unclaim`, not
				// `bd unclaim --force`. Refusal copy that names one gets
				// pattern-matched by batch agents into an unclaim+claim
				// steamroller of live claims (wy-yuclk). Point at the holder;
				// bd reclaim is safe to name because it only recovers claims
				// whose lease has already expired. Keep the %w wrap so this
				// open-but-assigned refusal is still a classifiable claim
				// conflict: the public IssueClaimer contract promises wrapped
				// ErrAlreadyClaimed, and errors.Is / ParseClaimConflict (and the
				// proxied batch exit code) key on it. This mirrors the
				// domain-stack twin (domain.issueUseCaseImpl.claim, bd-at6rc),
				// which already wraps the same message.
				return nil, fmt.Errorf("%w: already assigned to %q — coordinate with the holder; if their claim is abandoned (crashed agent), lease expiry will surface it for bd reclaim", storage.ErrAlreadyClaimed, assignee)
			}
			return nil, fmt.Errorf("%w%s%s", storage.ErrAlreadyClaimed, storage.ClaimedByFragment, assignee)
		}
		return nil, fmt.Errorf("%w%s%s", storage.ErrNotClaimable, storage.NotClaimableStatusFragment, currentStatus)
	}

	// Grant the lease: what makes the claim recoverable — a worker that dies
	// stops heartbeating and bd reclaim later reverts the issue. Lease rows
	// live in the ephemeral leases table (no Dolt commit, node-local). Wisps
	// are never leased (they are ephemeral, not reclaimable work).
	if !isWisp {
		if err := UpsertLeaseInTx(ctx, tx, id, actor, now, leaseTTL(ctx)); err != nil {
			return nil, err
		}
	}

	// Record the claim event.
	oldData, _ := json.Marshal(oldIssue)
	newUpdates := map[string]interface{}{
		"assignee": actor,
		"status":   "in_progress",
	}
	newData, _ := json.Marshal(newUpdates)

	if err := RecordFullEventInTable(ctx, tx, eventTable, id, types.EventClaimed, actor, string(oldData), string(newData)); err != nil {
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
	// Claim only ever delivers the one issue it successfully claims below —
	// the breaker's job is to bound delivered payloads, and a claim's
	// payload is always exactly one row regardless of how large the ready
	// pool it scanned was. So a rig-wide BEADS_MAX_ROWS/--max-rows cap
	// (sized for bulk list/ready reads) must not fire here, and the scan
	// itself must stay unbounded (Limit=0): the loop below walks
	// readyIssues in order and continues past any that are transiently
	// unclaimable (already claimed by a racing agent, etc), so bounding
	// the scan to Limit=MaxRows (e.g. BEADS_MAX_ROWS=1 → scan only the
	// single top-of-queue row) would make claim spuriously return "nothing
	// to claim" whenever that narrow window is unclaimable, even with
	// plenty of other ready work available. Clear the cap fields so
	// GetReadyWorkInTx never returns ErrTooManyRows either.
	//
	// This is parity with pre-PR main (which never bounded the claim scan)
	// and correctness-first: an unbounded scan preserves claim's existing
	// fairness/ordering guarantee across the whole ready set. A paged scan
	// that stays bounded while still walking past unclaimable rows is a
	// reasonable follow-up, but it's a genuine behavior change (not a
	// MaxRows-cap fix) and is deliberately deferred rather than folded in
	// here.
	claimFilter.Limit = 0
	claimFilter.MaxRows = 0
	claimFilter.MaxRowsSource = ""

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

// ClaimPoolAliasesInTx returns the pool pseudo-assignee aliases from the
// claim.pools config key (comma-separated, whitespace-trimmed). An issue
// assigned to one of these aliases is claimable by ANY actor through the
// normal claim CAS — the pattern where a dispatcher pre-assigns work to a
// group alias (e.g. "fable-crew") and members take items from the pool.
// Issues assigned to a real actor are unaffected. Missing/empty config (the
// default) disables pool-aware claiming entirely.
func ClaimPoolAliasesInTx(ctx context.Context, tx DBTX) ([]string, error) {
	raw, err := GetConfigInTx(ctx, tx, "claim.pools")
	if err != nil {
		return nil, err
	}
	var pools []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			pools = append(pools, p)
		}
	}
	return pools, nil
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
