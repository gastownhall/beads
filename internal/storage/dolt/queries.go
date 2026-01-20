package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// SearchIssues finds issues matching query and filters
func (s *DoltStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereClauses := []string{}
	args := []interface{}{}

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleSearch+"%")
	}

	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleContains+"%")
	}
	if filter.DescriptionContains != "" {
		whereClauses = append(whereClauses, "description LIKE ?")
		args = append(args, "%"+filter.DescriptionContains+"%")
	}
	if filter.NotesContains != "" {
		whereClauses = append(whereClauses, "notes LIKE ?")
		args = append(args, "%"+filter.NotesContains+"%")
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	} else if !filter.IncludeTombstones {
		whereClauses = append(whereClauses, "status != ?")
		args = append(args, types.StatusTombstone)
	}

	if len(filter.ExcludeStatus) > 0 {
		placeholders := make([]string, len(filter.ExcludeStatus))
		for i, s := range filter.ExcludeStatus {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("status NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	if len(filter.ExcludeTypes) > 0 {
		placeholders := make([]string, len(filter.ExcludeTypes))
		for i, t := range filter.ExcludeTypes {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("issue_type NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}
	if filter.PriorityMin != nil {
		whereClauses = append(whereClauses, "priority >= ?")
		args = append(args, *filter.PriorityMin)
	}
	if filter.PriorityMax != nil {
		whereClauses = append(whereClauses, "priority <= ?")
		args = append(args, *filter.PriorityMax)
	}

	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, *filter.IssueType)
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	// Date ranges
	if filter.CreatedAfter != nil {
		whereClauses = append(whereClauses, "created_at > ?")
		args = append(args, filter.CreatedAfter.Format(time.RFC3339))
	}
	if filter.CreatedBefore != nil {
		whereClauses = append(whereClauses, "created_at < ?")
		args = append(args, filter.CreatedBefore.Format(time.RFC3339))
	}
	if filter.UpdatedAfter != nil {
		whereClauses = append(whereClauses, "updated_at > ?")
		args = append(args, filter.UpdatedAfter.Format(time.RFC3339))
	}
	if filter.UpdatedBefore != nil {
		whereClauses = append(whereClauses, "updated_at < ?")
		args = append(args, filter.UpdatedBefore.Format(time.RFC3339))
	}

	// Empty/null checks
	if filter.EmptyDescription {
		whereClauses = append(whereClauses, "(description IS NULL OR description = '')")
	}
	if filter.NoAssignee {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	}
	if filter.NoLabels {
		whereClauses = append(whereClauses, "id NOT IN (SELECT DISTINCT issue_id FROM labels)")
	}

	// Label filtering (AND)
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}

	// Label filtering (OR)
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM labels WHERE label IN (%s))", strings.Join(placeholders, ", ")))
	}

	// ID filtering
	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", ")))
	}

	if filter.IDPrefix != "" {
		whereClauses = append(whereClauses, "id LIKE ?")
		args = append(args, filter.IDPrefix+"%")
	}

	// Wisp filtering
	if filter.Ephemeral != nil {
		if *filter.Ephemeral {
			whereClauses = append(whereClauses, "ephemeral = 1")
		} else {
			whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
		}
	}

	// Pinned filtering
	if filter.Pinned != nil {
		if *filter.Pinned {
			whereClauses = append(whereClauses, "pinned = 1")
		} else {
			whereClauses = append(whereClauses, "(pinned = 0 OR pinned IS NULL)")
		}
	}

	// Template filtering
	if filter.IsTemplate != nil {
		if *filter.IsTemplate {
			whereClauses = append(whereClauses, "is_template = 1")
		} else {
			whereClauses = append(whereClauses, "(is_template = 0 OR is_template IS NULL)")
		}
	}

	// Parent filtering
	if filter.ParentID != nil {
		whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?)")
		args = append(args, *filter.ParentID)
	}

	// Molecule type filtering
	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}

	// Time-based scheduling filters
	if filter.Deferred {
		whereClauses = append(whereClauses, "defer_until IS NOT NULL")
	}
	if filter.Overdue {
		whereClauses = append(whereClauses, "due_at IS NOT NULL AND due_at < ? AND status != ?")
		args = append(args, time.Now().UTC().Format(time.RFC3339), types.StatusClosed)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	// nolint:gosec // G201: whereSQL contains column comparisons with ?, limitSQL is a safe integer
	querySQL := fmt.Sprintf(`
		SELECT id FROM issues
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}
	defer rows.Close()

	return s.scanIssueIDs(ctx, rows)
}

// GetReadyWork returns issues that are ready to work on (not blocked)
func (s *DoltStore) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereClauses := []string{"status = 'open'", "(ephemeral = 0 OR ephemeral IS NULL)"}
	args := []interface{}{}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}
	if filter.Type != "" {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, filter.Type)
	}
	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}

	// Exclude blocked issues using subquery
	whereClauses = append(whereClauses, `
		id NOT IN (
			SELECT DISTINCT d.issue_id
			FROM dependencies d
			JOIN issues blocker ON d.depends_on_id = blocker.id
			WHERE d.type = 'blocks'
			  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		)
	`)

	whereSQL := "WHERE " + strings.Join(whereClauses, " AND ")

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	// nolint:gosec // G201: whereSQL contains column comparisons with ?, limitSQL is a safe integer
	query := fmt.Sprintf(`
		SELECT id FROM issues
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready work: %w", err)
	}
	defer rows.Close()

	return s.scanIssueIDs(ctx, rows)
}

// GetBlockedIssues returns issues that are blocked by other issues
func (s *DoltStore) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, COUNT(d.depends_on_id) as blocked_by_count
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		GROUP BY i.id
		ORDER BY i.priority ASC, i.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocked issues: %w", err)
	}
	defer rows.Close()

	var results []*types.BlockedIssue
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}

		issue, err := s.GetIssue(ctx, id)
		if err != nil || issue == nil {
			continue
		}

		// Get blocker IDs
		var blockerIDs []string
		blockerRows, err := s.db.QueryContext(ctx, `
			SELECT d.depends_on_id
			FROM dependencies d
			JOIN issues blocker ON d.depends_on_id = blocker.id
			WHERE d.issue_id = ?
			  AND d.type = 'blocks'
			  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		`, id)
		if err != nil {
			return nil, err
		}
		for blockerRows.Next() {
			var blockerID string
			if err := blockerRows.Scan(&blockerID); err != nil {
				_ = blockerRows.Close() // nolint:gosec // G104: error ignored on early return
				return nil, err
			}
			blockerIDs = append(blockerIDs, blockerID)
		}
		_ = blockerRows.Close() // nolint:gosec // G104: rows already read successfully

		results = append(results, &types.BlockedIssue{
			Issue:          *issue,
			BlockedByCount: count,
			BlockedBy:      blockerIDs,
		})
	}

	return results, rows.Err()
}

// GetEpicsEligibleForClosure returns epics whose children are all closed
func (s *DoltStore) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id,
		       (SELECT COUNT(*) FROM dependencies d JOIN issues c ON d.issue_id = c.id
		        WHERE d.depends_on_id = e.id AND d.type = 'parent-child') as total_children,
		       (SELECT COUNT(*) FROM dependencies d JOIN issues c ON d.issue_id = c.id
		        WHERE d.depends_on_id = e.id AND d.type = 'parent-child' AND c.status = 'closed') as closed_children
		FROM issues e
		WHERE e.issue_type = 'epic'
		  AND e.status != 'closed'
		  AND e.status != 'tombstone'
		HAVING total_children > 0 AND total_children = closed_children
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get epics eligible for closure: %w", err)
	}
	defer rows.Close()

	var results []*types.EpicStatus
	for rows.Next() {
		var id string
		var total, closed int
		if err := rows.Scan(&id, &total, &closed); err != nil {
			return nil, err
		}

		issue, err := s.GetIssue(ctx, id)
		if err != nil || issue == nil {
			continue
		}

		results = append(results, &types.EpicStatus{
			Epic:             issue,
			TotalChildren:    total,
			ClosedChildren:   closed,
			EligibleForClose: total > 0 && total == closed,
		})
	}

	return results, rows.Err()
}

// GetStaleIssues returns issues that haven't been updated recently
func (s *DoltStore) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -filter.Days)

	statusClause := "status IN ('open', 'in_progress')"
	if filter.Status != "" {
		statusClause = "status = ?"
	}

	// nolint:gosec // G201: statusClause contains only literal SQL or a single ? placeholder
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

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get stale issues: %w", err)
	}
	defer rows.Close()

	return s.scanIssueIDs(ctx, rows)
}

// GetStatistics returns summary statistics
func (s *DoltStore) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	stats := &types.Statistics{}

	// Get counts (mirror SQLite semantics: exclude tombstones from TotalIssues, report separately).
	// Important: COALESCE to avoid NULL scans when the table is empty.
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status != 'tombstone' THEN 1 ELSE 0 END), 0) as total,
			COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0) as open_count,
			COALESCE(SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END), 0) as in_progress,
			COALESCE(SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END), 0) as closed,
			COALESCE(SUM(CASE WHEN status = 'deferred' THEN 1 ELSE 0 END), 0) as deferred,
			COALESCE(SUM(CASE WHEN status = 'tombstone' THEN 1 ELSE 0 END), 0) as tombstone,
			COALESCE(SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END), 0) as pinned
		FROM issues
	`).Scan(
		&stats.TotalIssues,
		&stats.OpenIssues,
		&stats.InProgressIssues,
		&stats.ClosedIssues,
		&stats.DeferredIssues,
		&stats.TombstoneIssues,
		&stats.PinnedIssues,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get statistics: %w", err)
	}

	// Blocked count (same semantics as SQLite: blocked by open deps).
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT i.id)
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
	`).Scan(&stats.BlockedIssues)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocked count: %w", err)
	}

	// Ready count (use the ready_issues view).
	// Note: view already excludes ephemeral issues and blocked transitive deps.
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ready_issues`).Scan(&stats.ReadyIssues)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready count: %w", err)
	}

	return stats, nil
}

// GetMoleculeProgress returns progress stats for a molecule
func (s *DoltStore) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	stats := &types.MoleculeProgressStats{
		MoleculeID: moleculeID,
	}

	// Get molecule title
	var title sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", moleculeID).Scan(&title)
	if err == nil && title.Valid {
		stats.MoleculeTitle = title.String
	}

	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END) as completed,
			SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END) as in_progress
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		WHERE d.depends_on_id = ?
		  AND d.type = 'parent-child'
	`, moleculeID).Scan(&stats.Total, &stats.Completed, &stats.InProgress)

	if err != nil {
		return nil, fmt.Errorf("failed to get molecule progress: %w", err)
	}

	// Get first in_progress step ID
	var stepID sql.NullString
	_ = s.db.QueryRowContext(ctx, `
		SELECT i.id FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		WHERE d.depends_on_id = ?
		  AND d.type = 'parent-child'
		  AND i.status = 'in_progress'
		ORDER BY i.created_at ASC
		LIMIT 1
	`, moleculeID).Scan(&stepID)
	if stepID.Valid {
		stats.CurrentStepID = stepID.String
	}

	return stats, nil
}

// GetNextChildID returns the next available child ID for a parent
func (s *DoltStore) GetNextChildID(ctx context.Context, parentID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Get or create counter
	var lastChild int
	err = tx.QueryRowContext(ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", parentID).Scan(&lastChild)
	if err == sql.ErrNoRows {
		lastChild = 0
	} else if err != nil {
		return "", err
	}

	nextChild := lastChild + 1

	_, err = tx.ExecContext(ctx, `
		INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE last_child = ?
	`, parentID, nextChild, nextChild)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s.%d", parentID, nextChild), nil
}
