package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// ListVersionsInTx returns every retained version of issueID, newest first.
//
// No subquery wrapper here, deliberately: HistoryInTx wraps its select
// because dolt_history_issues is a Dolt SYSTEM table and the planner's
// max1Row optimization mis-assumes WHERE id=? yields one row. issue_versions
// is an ordinary user table with a composite (issue_id, revision) primary
// key, so that optimization does not apply and the wrapper would only cost a
// materialization.
//
// An issue with no versions returns an empty slice and no error -- that is a
// truthful "nothing retained here", and it is the CALLER's job to distinguish
// it from "the feature is off" and from "no such issue", which it cannot do
// from this result alone.
func ListVersionsInTx(ctx context.Context, tx DBTX, issueID string) ([]storage.IssueVersion, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			issue_id, revision, epoch,
			COALESCE(change_actor, '')        AS change_actor,
			COALESCE(change_agent, '')        AS change_agent,
			COALESCE(change_message, '')      AS change_message,
			change_at,
			COALESCE(attribution_status, '')  AS attribution_status,
			removed_at,
			COALESCE(removed_reason, '')      AS removed_reason,
			COALESCE(removed_restriction, '') AS removed_restriction,
			COALESCE(LENGTH(durable_state), 0) AS state_bytes
		FROM issue_versions
		WHERE issue_id = ?
		ORDER BY revision DESC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to list issue versions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []storage.IssueVersion
	for rows.Next() {
		var v storage.IssueVersion
		var removedAt sql.NullTime
		if err := rows.Scan(
			&v.IssueID, &v.Revision, &v.Epoch,
			&v.ChangeActor, &v.ChangeAgent, &v.ChangeMessage, &v.ChangeAt,
			&v.AttributionStatus,
			&removedAt, &v.RemovedReason, &v.RemovedRestriction,
			&v.StateBytes,
		); err != nil {
			return nil, fmt.Errorf("failed to scan issue version: %w", err)
		}
		if removedAt.Valid {
			t := removedAt.Time
			v.RemovedAt = &t
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read issue versions: %w", err)
	}
	return out, nil
}
