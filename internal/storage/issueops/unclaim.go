package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// UnclaimIssueInTx atomically unclaims an issue.
// Sets assignee to "" and status to "open".
// Records an "unclaimed" event.
// Only works on issues that have an assignee and status is "open" or "in_progress".
// Returns error if:
//   - Issue is closed (cannot unclaim closed issues)
//   - Issue has no assignee (nothing to unclaim)
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueInTx(ctx context.Context, tx *sql.Tx, id string, actor string) error {
	// Read current issue
	issueTable := "issues"
	eventTable := "events"

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

	now := time.Now().UTC()

	// Atomic UPDATE: clear assignee and reset status to open
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = 'open', updated_at = ?
		WHERE id = ? AND assignee != '' AND status IN ('open', 'in_progress')
	`, issueTable), now, id)
	if err != nil {
		return fmt.Errorf("failed to unclaim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
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
