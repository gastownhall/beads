package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// GetStaleIssuesInTx returns issues that haven't been updated within the
// given number of days. Only non-ephemeral issues are considered. When
// filter.Status is empty, open and in_progress issues are returned.
// Results are ordered by updated_at ascending (stalest first).
//
// nolint:gosec // G201: statusClause contains only literal SQL or a single ? placeholder
func GetStaleIssuesInTx(ctx context.Context, tx *sql.Tx, filter types.StaleFilter) ([]*types.Issue, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -filter.Days)

	statusClause := "status IN ('open', 'in_progress')"
	if filter.Status != "" {
		statusClause = "status = ?"
	}

	// Heartbeats live in the ephemeral leases table and no longer stamp
	// issues.updated_at (bd-lrgn1), so an actively-worked claim can carry an
	// old updated_at: an issue with a heartbeat since the cutoff is not stale.
	query := fmt.Sprintf(`
		SELECT id FROM issues
		WHERE updated_at < ?
		  AND %s
		  AND (ephemeral = 0 OR ephemeral IS NULL)
		  AND NOT EXISTS (
			SELECT 1 FROM leases WHERE leases.issue_id = issues.id AND leases.heartbeat_at >= ?
		  )
		ORDER BY updated_at ASC
	`, statusClause)
	args := []interface{}{cutoff}
	if filter.Status != "" {
		args = append(args, filter.Status)
	}
	args = append(args, cutoff) // NOT EXISTS heartbeat cutoff, after any status arg

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get stale issues: %w", err)
	}

	// Collect IDs first, then batch-fetch full issues.
	// Close rows explicitly before the nested fetch — MySQL/Dolt drivers
	// can't handle multiple active result sets on one connection.
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan stale issue id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("stale issues rows: %w", err)
	}
	rows.Close()

	if len(ids) == 0 {
		return nil, nil
	}

	// GetIssuesByIDsInTx returns issues in arbitrary order (WHERE IN),
	// so re-order to preserve the updated_at ASC ordering from the query.
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids, nil)
	if err != nil {
		return nil, err
	}

	issueByID := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		if iss != nil {
			issueByID[iss.ID] = iss
		}
	}

	ordered := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		if iss, ok := issueByID[id]; ok {
			ordered = append(ordered, iss)
		}
	}

	return ordered, nil
}
