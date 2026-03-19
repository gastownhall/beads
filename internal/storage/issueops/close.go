package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// CloseResult holds the result of a CloseIssueInTx call.
type CloseResult struct {
	IsWisp bool
}

// CloseIssueInTx closes an issue within a transaction, setting status to closed
// and recording the close event. Routes to the correct table (issues/wisps)
// automatically. The caller is responsible for Dolt versioning if needed.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func CloseIssueInTx(ctx context.Context, tx *sql.Tx, id string, reason, actor, session string) (*CloseResult, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	now := time.Now().UTC()

	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?
		WHERE id = ?
	`, issueTable), types.StatusClosed, now, now, reason, session, id)
	if err != nil {
		return nil, fmt.Errorf("failed to close issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return nil, fmt.Errorf("issue not found: %s", id)
	}

	if err := RecordEventInTable(ctx, tx, eventTable, id, types.EventClosed, actor, reason); err != nil {
		return nil, fmt.Errorf("failed to record event: %w", err)
	}

	return &CloseResult{IsWisp: isWisp}, nil
}
