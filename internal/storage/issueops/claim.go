package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ClaimFromStatusesKey is the config key listing additional statuses (beyond
// the built-in "open") from which an issue may be claimed. Projects that model
// their lifecycle with custom statuses (e.g. "ready") set this so that claim
// works for those statuses too. See gastownhall/beads#4164.
const ClaimFromStatusesKey = "claim.from-statuses"

// ClaimResult holds the result of a ClaimIssueInTx call.
type ClaimResult struct {
	OldIssue *types.Issue
	IsWisp   bool
}

// resolveClaimableStatusesInTx returns the set of statuses from which an issue
// may be claimed. The built-in "open" status is always claimable; projects can
// add others (typically custom statuses such as "ready") via the
// claim.from-statuses config key. The returned slice is de-duplicated and
// always contains "open" first so default behavior is unchanged when the key is
// unset (gastownhall/beads#4164).
func resolveClaimableStatusesInTx(ctx context.Context, tx *sql.Tx) []string {
	statuses := []string{string(types.StatusOpen)}
	seen := map[string]struct{}{string(types.StatusOpen): {}}

	value, err := GetConfigInTx(ctx, tx, ClaimFromStatusesKey)
	if err != nil || value == "" {
		return statuses
	}
	for _, s := range ParseCommaSeparatedList(value) {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		statuses = append(statuses, s)
	}
	return statuses
}

// ClaimIssueInTx atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// is currently in a claimable status (always "open", plus any statuses listed
// in the claim.from-statuses config key — see GH#4164) and is unassigned or
// already assigned to the same actor.
// Returns storage.ErrAlreadyClaimed if already claimed by a different user.
// Idempotent: re-claiming an in_progress issue by the same actor is a no-op
// success (supports agent retry workflows).
// Routes to the correct table (issues/wisps) automatically.
// The caller is responsible for Dolt versioning (DOLT_ADD/COMMIT) if needed.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func ClaimIssueInTx(ctx context.Context, tx *sql.Tx, id string, actor string) (*ClaimResult, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	// Read old issue inside the transaction for event recording.
	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue for claim: %w", err)
	}

	now := time.Now().UTC()

	// Build the claimable-status CAS predicate. "open" is always claimable;
	// projects may add custom statuses via claim.from-statuses (GH#4164).
	claimable := resolveClaimableStatusesInTx(ctx, tx)
	statusPlaceholders := strings.TrimSuffix(strings.Repeat("?, ", len(claimable)), ", ")
	statusArgs := make([]interface{}, len(claimable))
	for i, s := range claimable {
		statusArgs[i] = s
	}

	// Conditional UPDATE: only succeeds while the issue is still claimable.
	// Also set started_at on first transition to in_progress (GH#2796); preserve
	// any existing value so re-claims don't overwrite the original start time.
	var (
		result sql.Result
	)
	if oldIssue.StartedAt == nil {
		args := make([]interface{}, 0, 4+len(statusArgs))
		args = append(args, actor, now, now, id)
		args = append(args, statusArgs...)
		args = append(args, actor)
		result, err = tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, status = 'in_progress', updated_at = ?, started_at = ?
			WHERE id = ? AND status IN (%s) AND (assignee = '' OR assignee IS NULL OR assignee = ?)
		`, issueTable, statusPlaceholders), args...)
	} else {
		args := make([]interface{}, 0, 3+len(statusArgs))
		args = append(args, actor, now, id)
		args = append(args, statusArgs...)
		args = append(args, actor)
		result, err = tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, status = 'in_progress', updated_at = ?
			WHERE id = ? AND status IN (%s) AND (assignee = '' OR assignee IS NULL OR assignee = ?)
		`, issueTable, statusPlaceholders), args...)
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
	tx *sql.Tx,
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
