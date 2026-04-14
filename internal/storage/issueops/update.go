package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IsAllowedUpdateField checks if a field name is valid for issue updates.
func IsAllowedUpdateField(key string) bool {
	allowed := map[string]bool{
		"status": true, "priority": true, "title": true, "assignee": true,
		"description": true, "design": true, "acceptance_criteria": true, "notes": true,
		"issue_type": true, "estimated_minutes": true, "external_ref": true, "spec_id": true,
		"started_at": true,
		"closed_at":  true, "close_reason": true, "closed_by_session": true,
		"source_repo": true,
		"sender":      true, "wisp": true, "wisp_type": true, "no_history": true, "pinned": true,
		"mol_type":       true,
		"event_category": true, "event_actor": true, "event_target": true, "event_payload": true,
		"due_at": true, "defer_until": true, "await_id": true, "waiters": true,
		"metadata": true,
	}
	return allowed[key]
}

// ManageClosedAt auto-sets closed_at when closing or clears it when reopening.
func ManageClosedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]
	_, hasExplicitClosedAt := updates["closed_at"]
	if hasExplicitClosedAt || !hasStatus {
		return setClauses, args
	}

	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return setClauses, args
	}

	if newStatus == string(types.StatusClosed) {
		now := time.Now().UTC()
		setClauses = append(setClauses, "closed_at = ?")
		args = append(args, now)
	} else if oldIssue.Status == types.StatusClosed {
		setClauses = append(setClauses, "closed_at = ?", "close_reason = ?")
		args = append(args, nil, "")
	}

	return setClauses, args
}

// ManageStartedAt auto-sets started_at when transitioning to in_progress.
// If the issue already has a started_at, it is preserved (not overwritten).
func ManageStartedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]
	_, hasExplicitStartedAt := updates["started_at"]
	if hasExplicitStartedAt || !hasStatus {
		return setClauses, args
	}

	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return setClauses, args
	}

	if newStatus == string(types.StatusInProgress) && oldIssue.StartedAt == nil {
		now := time.Now().UTC()
		setClauses = append(setClauses, "started_at = ?")
		args = append(args, now)
	}

	return setClauses, args
}

// DetermineEventType returns the appropriate event type for an update.
func DetermineEventType(oldIssue *types.Issue, updates map[string]interface{}) types.EventType {
	statusVal, hasStatus := updates["status"]
	if !hasStatus {
		return types.EventUpdated
	}

	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return types.EventUpdated
	}

	if newStatus == string(types.StatusClosed) {
		return types.EventClosed
	}
	if oldIssue.Status == types.StatusClosed {
		return types.EventReopened
	}
	return types.EventStatusChanged
}

// UpdateResult holds the result of an UpdateIssueInTx call.
type UpdateResult struct {
	OldIssue *types.Issue
	IsWisp   bool
}

// UpdateIssueInTx performs the full update SQL logic within a transaction.
// It routes to the correct table (issues/wisps) automatically.
// The caller is responsible for Dolt versioning (DOLT_ADD/COMMIT) if needed.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UpdateIssueInTx(ctx context.Context, tx *sql.Tx, id string, updates map[string]interface{}, actor string) (*UpdateResult, error) {
	// Route to correct table.
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	// Read old issue inside the transaction for consistency.
	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue for update: %w", err)
	}

	// Build SET clauses.
	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now().UTC()}

	for key, value := range updates {
		if !IsAllowedUpdateField(key) {
			return nil, fmt.Errorf("invalid field for update: %s", key)
		}

		columnName := key
		if key == "wisp" {
			columnName = "ephemeral"
		}
		setClauses = append(setClauses, fmt.Sprintf("`%s` = ?", columnName))

		// Handle JSON serialization for array fields stored as TEXT.
		if key == "waiters" {
			waitersJSON, _ := json.Marshal(value)
			args = append(args, string(waitersJSON))
		} else if key == "metadata" {
			metadataStr, err := storage.NormalizeMetadataValue(value)
			if err != nil {
				return nil, fmt.Errorf("invalid metadata: %w", err)
			}
			args = append(args, metadataStr)
		} else {
			args = append(args, value)
		}
	}

	// Auto-clear pinned column when status transitions away from "pinned".
	if rawStatus, ok := updates["status"]; ok {
		var statusStr string
		switch v := rawStatus.(type) {
		case string:
			statusStr = v
		case types.Status:
			statusStr = string(v)
		}
		if oldIssue.Pinned && statusStr != string(types.StatusPinned) {
			if _, alreadySet := updates["pinned"]; !alreadySet {
				setClauses = append(setClauses, "`pinned` = ?")
				args = append(args, false)
			}
		}
	}

	// Auto-manage closed_at (set on close, clear on reopen).
	setClauses, args = ManageClosedAt(oldIssue, updates, setClauses, args)

	// Auto-manage started_at (set on transition to in_progress). (GH#2796)
	setClauses, args = ManageStartedAt(oldIssue, updates, setClauses, args)

	args = append(args, id)

	//nolint:gosec // G201: issueTable comes from WispTableRouting (hardcoded constants)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", issueTable, strings.Join(setClauses, ", "))
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return nil, fmt.Errorf("failed to update issue: %w", err)
	}

	// Record event.
	oldData, _ := json.Marshal(oldIssue)
	newData, _ := json.Marshal(updates)
	eventType := DetermineEventType(oldIssue, updates)

	if err := RecordFullEventInTable(ctx, tx, eventTable, id, eventType, actor, string(oldData), string(newData)); err != nil {
		return nil, fmt.Errorf("failed to record event: %w", err)
	}

	return &UpdateResult{OldIssue: oldIssue, IsWisp: isWisp}, nil
}

// RecordFullEventInTable records an event with both old and new values.
//
//nolint:gosec // G201: table is from WispTableRouting ("events" or "wisp_events")
func RecordFullEventInTable(ctx context.Context, tx *sql.Tx, table, issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, table), issueID, eventType, actor, oldValue, newValue)
	if err != nil {
		return fmt.Errorf("record event in %s: %w", table, err)
	}
	return nil
}
