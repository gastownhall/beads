package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetReadyWork returns issues with no open blockers
// By default, shows both 'open' and 'in_progress' issues so epics/tasks
// ready to close are visible (bd-165)
func (s *SQLiteStorage) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	whereClauses := []string{}
	args := []interface{}{}

	// Default to open OR in_progress if not specified (bd-165)
	if filter.Status == "" {
		whereClauses = append(whereClauses, "i.status IN ('open', 'in_progress')")
	} else {
		whereClauses = append(whereClauses, "i.status = ?")
		args = append(args, filter.Status)
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "i.priority = ?")
		args = append(args, *filter.Priority)
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "i.assignee = ?")
		args = append(args, *filter.Assignee)
	}

	// Label filtering (AND semantics)
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, `
				EXISTS (
					SELECT 1 FROM labels
					WHERE issue_id = i.id AND label = ?
				)
			`)
			args = append(args, label)
		}
	}

	// Label filtering (OR semantics)
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i := range filter.LabelsAny {
			placeholders[i] = "?"
		}
		whereClauses = append(whereClauses, fmt.Sprintf(`
			EXISTS (
				SELECT 1 FROM labels
				WHERE issue_id = i.id AND label IN (%s)
			)
		`, strings.Join(placeholders, ",")))
		for _, label := range filter.LabelsAny {
			args = append(args, label)
		}
	}

	// Build WHERE clause properly
	whereSQL := strings.Join(whereClauses, " AND ")

	// Build LIMIT clause using parameter
	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = " LIMIT ?"
		args = append(args, filter.Limit)
	}

	// Default to hybrid sort for backwards compatibility
	sortPolicy := filter.SortPolicy
	if sortPolicy == "" {
		sortPolicy = types.SortPolicyHybrid
	}
	orderBySQL := buildOrderByClause(sortPolicy)

	// Query with recursive CTE to propagate blocking through parent-child hierarchy
	// Algorithm:
	// 1. Find issues directly blocked by 'blocks' dependencies
	// 2. Recursively propagate blockage to all descendants via 'parent-child' links
	// 3. Exclude all blocked issues (both direct and transitive) from ready work
	// #nosec G201 - safe SQL with controlled formatting
	query := fmt.Sprintf(`
		WITH RECURSIVE
		  -- Step 1: Find issues blocked directly by dependencies
		  blocked_directly AS (
		    SELECT DISTINCT d.issue_id
		    FROM dependencies d
		    JOIN issues blocker ON d.depends_on_id = blocker.id
		    WHERE d.type = 'blocks'
		      AND blocker.status IN ('open', 'in_progress', 'blocked')
		  ),

		  -- Step 2: Propagate blockage to all descendants via parent-child
		  blocked_transitively AS (
		    -- Base case: directly blocked issues
		    SELECT issue_id, 0 as depth
		    FROM blocked_directly

		    UNION ALL

		    -- Recursive case: children of blocked issues inherit blockage
		    SELECT d.issue_id, bt.depth + 1
		    FROM blocked_transitively bt
		    JOIN dependencies d ON d.depends_on_id = bt.issue_id
		    WHERE d.type = 'parent-child'
		      AND bt.depth < 50
		  )

		-- Step 3: Select ready issues (excluding all blocked)
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		i.created_at, i.updated_at, i.closed_at, i.external_ref
		FROM issues i
		WHERE %s
		AND NOT EXISTS (
		SELECT 1 FROM blocked_transitively WHERE issue_id = i.id
		)
		%s
		%s
	`, whereSQL, orderBySQL, limitSQL)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready work: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
}

// GetStaleIssues returns issues that haven't been updated recently
func (s *SQLiteStorage) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	// Build query with optional status filter
	query := `
		SELECT
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref,
			compaction_level, compacted_at, compacted_at_commit, original_size
		FROM issues
		WHERE status != 'closed'
		  AND datetime(updated_at) < datetime('now', '-' || ? || ' days')
	`
	
	args := []interface{}{filter.Days}
	
	// Add optional status filter
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	
	query += " ORDER BY updated_at ASC"
	
	// Add limit
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query stale issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	
	var issues []*types.Issue
	for rows.Next() {
		var issue types.Issue
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString
		var contentHash sql.NullString
		var compactionLevel sql.NullInt64
		var compactedAt sql.NullTime
		var compactedAtCommit sql.NullString
		var originalSize sql.NullInt64
		
		err := rows.Scan(
			&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
			&compactionLevel, &compactedAt, &compactedAtCommit, &originalSize,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stale issue: %w", err)
		}
		
		if contentHash.Valid {
			issue.ContentHash = contentHash.String
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
		if compactionLevel.Valid {
			issue.CompactionLevel = int(compactionLevel.Int64)
		}
		if compactedAt.Valid {
			issue.CompactedAt = &compactedAt.Time
		}
		if compactedAtCommit.Valid {
			issue.CompactedAtCommit = &compactedAtCommit.String
		}
		if originalSize.Valid {
			issue.OriginalSize = int(originalSize.Int64)
		}
		
		issues = append(issues, &issue)
	}
	
	return issues, rows.Err()
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
	defer func() { _ = rows.Close() }()

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

// buildOrderByClause generates the ORDER BY clause based on sort policy
func buildOrderByClause(policy types.SortPolicy) string {
	switch policy {
	case types.SortPolicyPriority:
		return `ORDER BY i.priority ASC, i.created_at ASC`

	case types.SortPolicyOldest:
		return `ORDER BY i.created_at ASC`

	case types.SortPolicyHybrid:
		fallthrough
	default:
		return `ORDER BY
			CASE
				WHEN datetime(i.created_at) >= datetime('now', '-48 hours') THEN 0
				ELSE 1
			END ASC,
			CASE
				WHEN datetime(i.created_at) >= datetime('now', '-48 hours') THEN i.priority
				ELSE NULL
			END ASC,
			CASE
				WHEN datetime(i.created_at) < datetime('now', '-48 hours') THEN i.created_at
				ELSE NULL
			END ASC,
			i.created_at ASC`
	}
}
