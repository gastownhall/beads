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

	// Unassigned takes precedence over Assignee filter
	if filter.Unassigned {
		whereClauses = append(whereClauses, "(i.assignee IS NULL OR i.assignee = '')")
	} else if filter.Assignee != nil {
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

	// Use blocked_issues_cache for performance (bd-5qim)
	// This optimization replaces the recursive CTE that computed blocked issues on every query.
	// Performance improvement: 752ms → 29ms on 10K issues (25x speedup).
	//
	// The cache is automatically maintained by invalidateBlockedCache() which is called:
	//   - When adding/removing 'blocks' or 'parent-child' dependencies
	//   - When any issue status changes
	//   - When closing any issue
	//
	// Cache rebuild is fast (<50ms) and happens within the same transaction as the
	// triggering change, ensuring consistency. See blocked_cache.go for full details.
	// #nosec G201 - safe SQL with controlled formatting
	query := fmt.Sprintf(`
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		i.created_at, i.updated_at, i.closed_at, i.external_ref, i.source_repo, i.close_reason,
		i.deleted_at, i.deleted_by, i.delete_reason, i.original_type
		FROM issues i
		WHERE %s
		AND NOT EXISTS (
		  SELECT 1 FROM blocked_issues_cache WHERE issue_id = i.id
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
			created_at, updated_at, closed_at, external_ref, source_repo,
			compaction_level, compacted_at, compacted_at_commit, original_size, close_reason,
			deleted_at, deleted_by, delete_reason, original_type
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
		var sourceRepo sql.NullString
		var contentHash sql.NullString
		var compactionLevel sql.NullInt64
		var compactedAt sql.NullTime
		var compactedAtCommit sql.NullString
		var originalSize sql.NullInt64
		var closeReason sql.NullString
		var deletedAt sql.NullString // TEXT column, not DATETIME - must parse manually
		var deletedBy sql.NullString
		var deleteReason sql.NullString
		var originalType sql.NullString

		err := rows.Scan(
			&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef, &sourceRepo,
			&compactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &closeReason,
			&deletedAt, &deletedBy, &deleteReason, &originalType,
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
		if sourceRepo.Valid {
			issue.SourceRepo = sourceRepo.String
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
		if closeReason.Valid {
			issue.CloseReason = closeReason.String
		}
		issue.DeletedAt = parseNullableTimeString(deletedAt)
		if deletedBy.Valid {
			issue.DeletedBy = deletedBy.String
		}
		if deleteReason.Valid {
			issue.DeleteReason = deleteReason.String
		}
		if originalType.Valid {
			issue.OriginalType = originalType.String
		}

		issues = append(issues, &issue)
	}

	return issues, rows.Err()
}

// GetBlockedIssues returns issues that are blocked by dependencies or have status=blocked
func (s *SQLiteStorage) GetBlockedIssues(ctx context.Context) ([]*types.BlockedIssue, error) {
	// Use UNION to combine:
	// 1. Issues with open/in_progress/blocked status that have dependency blockers
	// 2. Issues with status=blocked (even if they have no dependency blockers)
	// Use GROUP_CONCAT to get all blocker IDs in a single query (no N+1)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
		    i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		    i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		    i.created_at, i.updated_at, i.closed_at, i.external_ref, i.source_repo,
		    COALESCE(COUNT(d.depends_on_id), 0) as blocked_by_count,
		    COALESCE(GROUP_CONCAT(d.depends_on_id, ','), '') as blocker_ids
		FROM issues i
		LEFT JOIN dependencies d ON i.id = d.issue_id
		    AND d.type = 'blocks'
		    AND EXISTS (
		        SELECT 1 FROM issues blocker
		        WHERE blocker.id = d.depends_on_id
		        AND blocker.status IN ('open', 'in_progress', 'blocked')
		    )
		WHERE i.status IN ('open', 'in_progress', 'blocked')
		  AND (
		      i.status = 'blocked'
		      OR EXISTS (
		          SELECT 1 FROM dependencies d2
		          JOIN issues blocker ON d2.depends_on_id = blocker.id
		          WHERE d2.issue_id = i.id
		            AND d2.type = 'blocks'
		            AND blocker.status IN ('open', 'in_progress', 'blocked')
		      )
		  )
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
		var sourceRepo sql.NullString
		var blockerIDsStr string

		err := rows.Scan(
			&issue.ID, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef, &sourceRepo, &issue.BlockedByCount,
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
		if sourceRepo.Valid {
			issue.SourceRepo = sourceRepo.String
		}

		// Parse comma-separated blocker IDs
		if blockerIDsStr != "" {
			issue.BlockedBy = strings.Split(blockerIDsStr, ",")
		} else {
			issue.BlockedBy = []string{}
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
