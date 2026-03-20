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

	query := fmt.Sprintf(`
		SELECT id FROM issues
		WHERE updated_at < ?
		  AND %s
		  AND (ephemeral = 0 OR ephemeral IS NULL)
		ORDER BY updated_at ASC
	`, statusClause)
	args := []interface{}{cutoff}
	if filter.Status != "" {
		args = append(args, filter.Status)
	}

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
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids)
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
