//go:build cgo

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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

	// Use subquery for type exclusion to prevent Dolt mergeJoinIter panic (same as IssueType above).
	if len(filter.ExcludeTypes) > 0 {
		placeholders := make([]string, len(filter.ExcludeTypes))
		for i, t := range filter.ExcludeTypes {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT id FROM issues WHERE issue_type NOT IN (%s))", strings.Join(placeholders, ",")))
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

	// Use subquery for type filter to prevent Dolt mergeJoinIter panic.
	// When issue_type equality is combined with other indexed predicates (status, priority)
	// in the same WHERE clause, Dolt's query optimizer may select a merge join plan
	// between index scans that panics in mergeJoinIter. Isolating the type predicate
	// in a subquery forces sequential evaluation and avoids the problematic plan.
	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "id IN (SELECT id FROM issues WHERE issue_type = ?)")
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
	if filter.SpecIDPrefix != "" {
		whereClauses = append(whereClauses, "spec_id LIKE ?")
		args = append(args, filter.SpecIDPrefix+"%")
	}

	// Source repo filtering
	if filter.SourceRepo != nil {
		whereClauses = append(whereClauses, "source_repo = ?")
		args = append(args, *filter.SourceRepo)
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

	// Parent filtering: filter children by parent issue
	// Also includes dotted-ID children (e.g., "parent.1.2" is child of "parent")
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR id LIKE CONCAT(?, '.%'))")
		args = append(args, parentID, parentID)
	}

	// Molecule type filtering
	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}

	// Wisp type filtering (TTL-based compaction classification)
	if filter.WispType != nil {
		whereClauses = append(whereClauses, "wisp_type = ?")
		args = append(args, string(*filter.WispType))
	}

	// Time-based scheduling filters
	if filter.Deferred {
		whereClauses = append(whereClauses, "defer_until IS NOT NULL")
	}
	if filter.Overdue {
		whereClauses = append(whereClauses, "due_at IS NOT NULL AND due_at < ? AND status != ?")
		args = append(args, time.Now().UTC().Format(time.RFC3339), types.StatusClosed)
	}
	if filter.ClosedAfter != nil {
		whereClauses = append(whereClauses, "closed_at > ?")
		args = append(args, filter.ClosedAfter.Format(time.RFC3339))
	}
	if filter.ClosedBefore != nil {
		whereClauses = append(whereClauses, "closed_at < ?")
		args = append(args, filter.ClosedBefore.Format(time.RFC3339))
	}
	if filter.DeferAfter != nil {
		whereClauses = append(whereClauses, "defer_until > ?")
		args = append(args, filter.DeferAfter.Format(time.RFC3339))
	}
	if filter.DeferBefore != nil {
		whereClauses = append(whereClauses, "defer_until < ?")
		args = append(args, filter.DeferBefore.Format(time.RFC3339))
	}
	if filter.DueAfter != nil {
		whereClauses = append(whereClauses, "due_at > ?")
		args = append(args, filter.DueAfter.Format(time.RFC3339))
	}
	if filter.DueBefore != nil {
		whereClauses = append(whereClauses, "due_at < ?")
		args = append(args, filter.DueBefore.Format(time.RFC3339))
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

	rows, err := s.queryContext(ctx, querySQL, args...)
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

	// Status filtering: default to open OR in_progress (matches memory storage)
	var statusClause string
	if filter.Status != "" {
		statusClause = "status = ?"
	} else {
		statusClause = "status IN ('open', 'in_progress')"
	}
	whereClauses := []string{statusClause, "(ephemeral = 0 OR ephemeral IS NULL)"}
	args := []interface{}{}
	if filter.Status != "" {
		args = append(args, string(filter.Status))
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}
	// Use subquery for type filter to prevent Dolt mergeJoinIter panic (see SearchIssues).
	if filter.Type != "" {
		whereClauses = append(whereClauses, "id IN (SELECT id FROM issues WHERE issue_type = ?)")
		args = append(args, filter.Type)
	}
	// Unassigned takes precedence over Assignee filter (matches memory storage)
	if filter.Unassigned {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	} else if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}
	// Exclude future-deferred issues unless IncludeDeferred is set
	if !filter.IncludeDeferred {
		whereClauses = append(whereClauses, "(defer_until IS NULL OR defer_until <= NOW())")
	}
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}

	// Exclude blocked issues using nested EXISTS to avoid Dolt mergeJoinIter panic
	whereClauses = append(whereClauses, `
		NOT EXISTS (
			SELECT 1 FROM dependencies d
			WHERE d.issue_id = issues.id
			  AND d.type = 'blocks'
			  AND EXISTS (
			    SELECT 1 FROM issues blocker
			    WHERE blocker.id = d.depends_on_id
			      AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
			  )
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

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready work: %w", err)
	}
	defer rows.Close()

	return s.scanIssueIDs(ctx, rows)
}

// GetBlockedIssues returns issues that are blocked by other issues.
// Uses separate single-table queries with Go-level filtering to avoid
// correlated EXISTS subqueries that trigger Dolt's joinIter panic
// (slice bounds out of range at join_iters.go:192).
// Same fix pattern as GetStatistics blocked count (fc16065c, a4a21958).
func (s *DoltStore) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Step 1: Get all open/active issue IDs into a set (single-table scan)
	activeIDs := make(map[string]bool)
	activeRows, err := s.queryContext(ctx, `
		SELECT id FROM issues
		WHERE status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get active issues: %w", err)
	}
	for activeRows.Next() {
		var id string
		if err := activeRows.Scan(&id); err != nil {
			_ = activeRows.Close()
			return nil, err
		}
		activeIDs[id] = true
	}
	_ = activeRows.Close()
	if err := activeRows.Err(); err != nil {
		return nil, err
	}

	// Step 2: Get all blocking dependencies (single-table scan)
	depRows, err := s.queryContext(ctx, `
		SELECT issue_id, depends_on_id FROM dependencies
		WHERE type = 'blocks'
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocking dependencies: %w", err)
	}

	// Step 3: Filter in Go — both sides must be active
	// blockerMap: blocked_issue_id -> list of active blocker IDs
	blockerMap := make(map[string][]string)
	for depRows.Next() {
		var issueID, blockerID string
		if err := depRows.Scan(&issueID, &blockerID); err != nil {
			_ = depRows.Close()
			return nil, err
		}
		if activeIDs[issueID] && activeIDs[blockerID] {
			blockerMap[issueID] = append(blockerMap[issueID], blockerID)
		}
	}
	_ = depRows.Close()
	if err := depRows.Err(); err != nil {
		return nil, err
	}

	// Step 4: Hydrate blocked issues and build results
	var results []*types.BlockedIssue
	for id, blockerIDs := range blockerMap {
		issue, err := s.GetIssue(ctx, id)
		if err != nil || issue == nil {
			continue
		}

		results = append(results, &types.BlockedIssue{
			Issue:          *issue,
			BlockedByCount: len(blockerIDs),
			BlockedBy:      blockerIDs,
		})
	}

	// Sort by priority ASC, then created_at DESC (matching original SQL ORDER BY)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Issue.Priority != results[j].Issue.Priority {
			return results[i].Issue.Priority < results[j].Issue.Priority
		}
		return results[i].Issue.CreatedAt.After(results[j].Issue.CreatedAt)
	})

	return results, nil
}

// GetEpicsEligibleForClosure returns epics whose children are all closed
func (s *DoltStore) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	rows, err := s.queryContext(ctx, `
		SELECT e.id,
		       COUNT(d.issue_id) as total_children,
		       COUNT(CASE WHEN c.status = 'closed' THEN 1 END) as closed_children
		FROM issues e
		LEFT JOIN dependencies d ON d.depends_on_id = e.id AND d.type = 'parent-child'
		LEFT JOIN issues c ON d.issue_id = c.id
		WHERE e.issue_type = 'epic'
		  AND e.status != 'closed'
		  AND e.status != 'tombstone'
		GROUP BY e.id
		HAVING total_children > 0
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get epics eligible for closure: %w", err)
	}
	defer rows.Close()

	// Collect IDs first, then close rows before fetching full issues.
	// This avoids connection pool deadlock when MaxOpenConns=1 (embedded dolt).
	type epicInfo struct {
		id            string
		total, closed int
	}
	var epics []epicInfo
	for rows.Next() {
		var info epicInfo
		if err := rows.Scan(&info.id, &info.total, &info.closed); err != nil {
			return nil, err
		}
		epics = append(epics, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	var results []*types.EpicStatus
	for _, info := range epics {
		issue, err := s.GetIssue(ctx, info.id)
		if err != nil || issue == nil {
			continue
		}

		results = append(results, &types.EpicStatus{
			Epic:             issue,
			TotalChildren:    info.total,
			ClosedChildren:   info.closed,
			EligibleForClose: info.total > 0 && info.total == info.closed,
		})
	}

	return results, nil
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

	rows, err := s.queryContext(ctx, query, args...)
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

	// Blocked count: use two separate queries to avoid multi-table joins
	// that trigger Dolt's mergeJoinIter panic (slice bounds out of range).
	// Step 1: Get IDs of open blockers
	// Step 2: Count distinct blocked issues from those blockers
	var blockedCount int
	blockerRows, err := s.queryContext(ctx, `
		SELECT DISTINCT d.issue_id
		FROM dependencies d
		WHERE d.type = 'blocks'
		  AND d.depends_on_id IN (
		    SELECT id FROM issues
		    WHERE status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  )
		  AND d.issue_id IN (
		    SELECT id FROM issues
		    WHERE status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  )
	`)
	if err == nil {
		defer blockerRows.Close()
		for blockerRows.Next() {
			var id string
			if err := blockerRows.Scan(&id); err == nil {
				blockedCount++
			}
		}
	}
	stats.BlockedIssues = blockedCount

	// Ready count: compute without using the ready_issues view to avoid
	// recursive CTE join that triggers the same Dolt panic.
	// Ready = open, non-ephemeral, not blocked (directly or transitively).
	stats.ReadyIssues = stats.OpenIssues - blockedCount
	if stats.ReadyIssues < 0 {
		stats.ReadyIssues = 0
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
