package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

type ReopenResult struct {
	IsWisp      bool
	AlreadyOpen bool
}

//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func ReopenIssueInTx(ctx context.Context, tx DBTX, id, reason, actor string) (*ReopenResult, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	var affectedIssues, affectedWisps []string
	var aerr error
	if isWisp {
		affectedIssues, affectedWisps, aerr = AffectedByStatusChangeForWispInTx(ctx, tx, id)
	} else {
		affectedIssues, affectedWisps, aerr = AffectedByStatusChangeInTx(ctx, tx, id)
	}
	if aerr != nil {
		return nil, fmt.Errorf("affected by reopen for %s: %w", id, aerr)
	}

	now := time.Now().UTC()

	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status = ?, closed_at = NULL, close_reason = '', closed_by_session = '', defer_until = NULL, updated_at = ?
		WHERE id = ? AND status = ?
	`, issueTable), types.StatusOpen, now, id, types.StatusClosed)
	if err != nil {
		return nil, fmt.Errorf("failed to reopen issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		var status string
		qerr := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT status FROM %s WHERE id = ?`, issueTable), id,
		).Scan(&status)
		if qerr == sql.ErrNoRows {
			return nil, fmt.Errorf("issue not found: %s", id)
		}
		if qerr != nil {
			return nil, fmt.Errorf("failed to check issue existence: %w", qerr)
		}
		if types.Status(status) != types.StatusClosed {
			return &ReopenResult{IsWisp: isWisp, AlreadyOpen: true}, nil
		}
		return nil, fmt.Errorf("failed to reopen issue: %s", id)
	}

	if err := RecordEventInTable(ctx, tx, eventTable, id, types.EventReopened, actor, reason); err != nil {
		return nil, fmt.Errorf("failed to record event: %w", err)
	}

	if reason != "" {
		if err := AddCommentEventInTx(ctx, tx, id, actor, reason); err != nil {
			return nil, fmt.Errorf("failed to add reopen comment: %w", err)
		}
	}

	if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return nil, fmt.Errorf("recompute is_blocked after reopen for %s: %w", id, err)
	}

	return &ReopenResult{IsWisp: isWisp}, nil
}
