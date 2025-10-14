package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetReadyWork returns issues with no open blockers
func (s *SQLiteStorage) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	whereClauses := []string{}
	args := []interface{}{}

	// Default to open status if not specified
	if filter.Status == "" {
		filter.Status = types.StatusOpen
	}

	whereClauses = append(whereClauses, "i.status = ?")
	args = append(args, filter.Status)

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "i.priority = ?")
		args = append(args, *filter.Priority)
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "i.assignee = ?")
		args = append(args, *filter.Assignee)
	}

	// Build WHERE clause properly
	whereSQL := strings.Join(whereClauses, " AND ")

	// Build LIMIT clause using parameter
	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = " LIMIT ?"
		args = append(args, filter.Limit)
	}

	// Single query template
	query := fmt.Sprintf(`
		SELECT i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.updated_at, i.closed_at, i.external_ref
		FROM issues i
		WHERE %s
		  AND NOT EXISTS (
		    SELECT 1 FROM dependencies d
		    JOIN issues blocked ON d.depends_on_id = blocked.id
		    WHERE d.issue_id = i.id
		      AND d.type = 'blocks'
		      AND blocked.status IN ('open', 'in_progress', 'blocked')
		  )
		ORDER BY i.priority ASC, i.created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready work: %w", err)
	}
	defer rows.Close()

	return scanIssues(rows)
}

// GetBlockedIssues returns issues that are blocked by dependencies
func (s *SQLiteStorage) GetBlockedIssues(ctx context.Context) ([]*types.BlockedIssue, error) {
	// Use GROUP_CONCAT to get all blocker IDs in a single query (no N+1)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
		    i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		    i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		    i.created_at, i.updated_at, i.closed_at, i.external_ref,
		    COUNT(d.depends_on_id) as blocked_by_count,
		    GROUP_CONCAT(d.depends_on_id, ',') as blocker_ids
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE i.status IN ('open', 'in_progress', 'blocked')
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked')
		GROUP BY i.id
		ORDER BY i.priority ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocked issues: %w", err)
	}
	defer rows.Close()

	var blocked []*types.BlockedIssue
	for rows.Next() {
		var issue types.BlockedIssue
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString
		var blockerIDsStr string

		err := rows.Scan(
			&issue.ID, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef, &issue.BlockedByCount,
			&blockerIDsStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan blocked issue: %w", err)
		}

		if closedAt.Valid {
			issue.ClosedAt = &closedAt.Time
		}
		if estimatedMinutes.Valid {
			mins := int(estimatedMinutes.Int64)
			issue.EstimatedMinutes = &mins
		}
		if assignee.Valid {
			issue.Assignee = assignee.String
		}
		if externalRef.Valid {
			issue.ExternalRef = &externalRef.String
		}

		// Parse comma-separated blocker IDs
		if blockerIDsStr != "" {
			issue.BlockedBy = strings.Split(blockerIDsStr, ",")
		}

		blocked = append(blocked, &issue)
	}

	return blocked, nil
}
