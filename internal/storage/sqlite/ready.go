package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// GetReadyWork returns issues with no open blockers
// By default, shows both 'open' and 'in_progress' issues so epics/tasks
// ready to close are visible.
// Excludes pinned issues which are persistent anchors, not actionable work.
func (s *SQLiteStorage) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	whereClauses := []string{
		"i.pinned = 0",                             // Exclude pinned issues
		"(i.ephemeral = 0 OR i.ephemeral IS NULL)", // Exclude wisps by ephemeral flag
	}
	args := []interface{}{}

	// Default to open OR in_progress if not specified
	if filter.Status == "" {
		whereClauses = append(whereClauses, "i.status IN ('open', 'in_progress')")
	} else {
		whereClauses = append(whereClauses, "i.status = ?")
		args = append(args, filter.Status)
	}

	// Filter by issue type for MQ integration
	if filter.Type != "" {
		whereClauses = append(whereClauses, "i.issue_type = ?")
		args = append(args, filter.Type)
	} else {
		// Exclude workflow types from ready work by default
		// These are internal workflow items, not work for polecats to claim:
		// - merge-request: processed by Refinery
		// - gate: async wait conditions
		// - molecule: workflow containers
		// - message: mail/communication items
		// - agent: identity/state tracking beads
		// - role: agent role definitions (reference metadata)
		// - rig: rig identity beads (reference metadata)
		whereClauses = append(whereClauses, "i.issue_type NOT IN ('merge-request', 'gate', 'molecule', 'message', 'agent', 'role', 'rig')")
		// Exclude IDs matching configured patterns
		// Default patterns: -mol- (molecule steps), -wisp- (ephemeral wisps)
		// Configure with: bd config set ready.exclude_id_patterns "-mol-,-wisp-"
		// Use --type=task to explicitly include them, or IncludeMolSteps for internal callers
		if !filter.IncludeMolSteps {
			patterns := s.getExcludeIDPatterns(ctx)
			for _, pattern := range patterns {
				whereClauses = append(whereClauses, "i.id NOT LIKE '%"+pattern+"%'")
			}
		}
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

	// Parent filtering: filter to all descendants of a root issue (epic/molecule)
	// Uses recursive CTE to find all descendants via parent-child dependencies
	if filter.ParentID != nil {
		whereClauses = append(whereClauses, `
			i.id IN (
				WITH RECURSIVE descendants AS (
					SELECT issue_id FROM dependencies
					WHERE type = 'parent-child' AND depends_on_id = ?
					UNION ALL
					SELECT d.issue_id FROM dependencies d
					JOIN descendants dt ON d.depends_on_id = dt.issue_id
					WHERE d.type = 'parent-child'
				)
				SELECT issue_id FROM descendants
			)
		`)
		args = append(args, *filter.ParentID)
	}

	// Molecule type filtering
	if filter.MolType != nil {
		whereClauses = append(whereClauses, "i.mol_type = ?")
		args = append(args, string(*filter.MolType))
	}

	// Time-based deferral filtering (GH#820)
	// By default, exclude issues where defer_until is in the future.
	// If IncludeDeferred is true, skip this filter to show deferred issues.
	if !filter.IncludeDeferred {
		whereClauses = append(whereClauses, "(i.defer_until IS NULL OR datetime(i.defer_until) <= datetime('now'))")
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

	// Use blocked_issues_cache for performance
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
		i.created_at, i.created_by, i.owner, i.updated_at, i.closed_at, i.external_ref, i.source_repo, i.close_reason,
		i.deleted_at, i.deleted_by, i.delete_reason, i.original_type,
		i.sender, i.ephemeral, i.pinned, i.is_template, i.crystallizes,
		i.await_type, i.await_id, i.timeout_ns, i.waiters,
		i.hook_bead, i.role_bead, i.agent_state, i.last_activity, i.role_type, i.rig, i.mol_type,
		i.due_at, i.defer_until
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

	issues, err := s.scanIssues(ctx, rows)
	if err != nil {
		return nil, err
	}

	// Filter out issues with unsatisfied external dependencies
	// Only check if external_projects are configured
	if len(config.GetExternalProjects()) > 0 && len(issues) > 0 {
		issues, err = s.filterByExternalDeps(ctx, issues)
		if err != nil {
			return nil, fmt.Errorf("failed to check external dependencies: %w", err)
		}
	}

	return issues, nil
}

// filterByExternalDeps removes issues that have unsatisfied external dependencies.
// External deps have format: external:<project>:<capability>
// They are satisfied when the target project has a closed issue with provides:<capability> label.
//
// Optimization: Collects all unique external refs across all issues, then checks
// them in batch (one DB open per external project) rather than checking each
// ref individually. This avoids O(N) DB opens when issues share external deps.
func (s *SQLiteStorage) filterByExternalDeps(ctx context.Context, issues []*types.Issue) ([]*types.Issue, error) {
	if len(issues) == 0 {
		return issues, nil
	}

	// Build list of issue IDs
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}

	// Batch query: get all external deps for these issues
	externalDeps, err := s.getExternalDepsForIssues(ctx, issueIDs)
	if err != nil {
		return nil, err
	}

	// If no external deps, return all issues
	if len(externalDeps) == 0 {
		return issues, nil
	}

	// Collect all unique external refs across all issues
	uniqueRefs := make(map[string]bool)
	for _, deps := range externalDeps {
		for _, dep := range deps {
			uniqueRefs[dep] = true
		}
	}

	// Check all refs in batch (grouped by project internally)
	refList := make([]string, 0, len(uniqueRefs))
	for ref := range uniqueRefs {
		refList = append(refList, ref)
	}
	statuses := CheckExternalDeps(ctx, refList)

	// Build set of blocked issue IDs using batch results
	blockedIssues := make(map[string]bool)
	for issueID, deps := range externalDeps {
		for _, dep := range deps {
			if status, ok := statuses[dep]; ok && !status.Satisfied {
				blockedIssues[issueID] = true
				break // One unsatisfied dep is enough to block
			}
		}
	}

	// Filter out blocked issues
	if len(blockedIssues) == 0 {
		return issues, nil
	}

	result := make([]*types.Issue, 0, len(issues)-len(blockedIssues))
	for _, issue := range issues {
		if !blockedIssues[issue.ID] {
			result = append(result, issue)
		}
	}

	return result, nil
}

// getExternalDepsForIssues returns a map of issue ID -> list of external dep refs
func (s *SQLiteStorage) getExternalDepsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// #nosec G201 -- placeholders are "?" literals, not user input
	query := fmt.Sprintf(`
		SELECT issue_id, depends_on_id
		FROM dependencies
		WHERE issue_id IN (%s)
		  AND type = 'blocks'
		  AND depends_on_id LIKE 'external:%%'
	`, strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query external dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string][]string)
	for rows.Next() {
		var issueID, depRef string
		if err := rows.Scan(&issueID, &depRef); err != nil {
			return nil, fmt.Errorf("failed to scan external dependency: %w", err)
		}
		result[issueID] = append(result[issueID], depRef)
	}

	return result, rows.Err()
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
			deleted_at, deleted_by, delete_reason, original_type,
			sender, ephemeral, pinned, is_template,
			await_type, await_id, timeout_ns, waiters
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
		var createdAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
		var updatedAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
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
		// Messaging fields
		var sender sql.NullString
		var ephemeral sql.NullInt64
		// Pinned field
		var pinned sql.NullInt64
		// Template field
		var isTemplate sql.NullInt64
		// Gate fields
		var awaitType sql.NullString
		var awaitID sql.NullString
		var timeoutNs sql.NullInt64
		var waiters sql.NullString

		err := rows.Scan(
			&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&createdAtStr, &updatedAtStr, &closedAt, &externalRef, &sourceRepo,
			&compactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &closeReason,
			&deletedAt, &deletedBy, &deleteReason, &originalType,
			&sender, &ephemeral, &pinned, &isTemplate,
			&awaitType, &awaitID, &timeoutNs, &waiters,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stale issue: %w", err)
		}

		// Parse timestamp strings (TEXT columns require manual parsing)
		if createdAtStr.Valid {
			issue.CreatedAt = parseTimeString(createdAtStr.String)
		}
		if updatedAtStr.Valid {
			issue.UpdatedAt = parseTimeString(updatedAtStr.String)
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
		// Messaging fields
		if sender.Valid {
			issue.Sender = sender.String
		}
		if ephemeral.Valid && ephemeral.Int64 != 0 {
			issue.Ephemeral = true
		}
		// Pinned field
		if pinned.Valid && pinned.Int64 != 0 {
			issue.Pinned = true
		}
		// Template field
		if isTemplate.Valid && isTemplate.Int64 != 0 {
			issue.IsTemplate = true
		}
		// Gate fields
		if awaitType.Valid {
			issue.AwaitType = awaitType.String
		}
		if awaitID.Valid {
			issue.AwaitID = awaitID.String
		}
		if timeoutNs.Valid {
			issue.Timeout = time.Duration(timeoutNs.Int64)
		}
		if waiters.Valid && waiters.String != "" {
			issue.Waiters = parseJSONStringArray(waiters.String)
		}

		issues = append(issues, &issue)
	}

	return issues, rows.Err()
}

// GetBlockedIssues returns issues that are blocked by dependencies or have status=blocked
// Note: Pinned issues are excluded from the output.
// Note: Includes external: references in blocked_by list.
func (s *SQLiteStorage) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	// Use UNION to combine:
	// 1. Issues with open/in_progress/blocked status that have dependency blockers
	// 2. Issues with status=blocked (even if they have no dependency blockers)
	// Use GROUP_CONCAT to get all blocker IDs in a single query (no N+1)
	// Exclude pinned issues.
	//
	// For blocked_by_count and blocker_ids:
	// - Count local blockers (open issues) + external refs (external:*)
	// - External refs are always considered "open" until resolved

	// Build additional WHERE clauses for filtering
	var filterClauses []string
	var args []any

	// Parent filtering: filter to all descendants of a root issue (epic/molecule)
	if filter.ParentID != nil {
		filterClauses = append(filterClauses, `
			i.id IN (
				WITH RECURSIVE descendants AS (
					SELECT issue_id FROM dependencies
					WHERE type = 'parent-child' AND depends_on_id = ?
					UNION ALL
					SELECT d.issue_id FROM dependencies d
					JOIN descendants dt ON d.depends_on_id = dt.issue_id
					WHERE d.type = 'parent-child'
				)
				SELECT issue_id FROM descendants
			)
		`)
		args = append(args, *filter.ParentID)
	}

	// Build filter clause SQL
	filterSQL := ""
	if len(filterClauses) > 0 {
		filterSQL = " AND " + strings.Join(filterClauses, " AND ")
	}

	// nolint:gosec // G201: filterSQL contains only parameterized WHERE clauses with ? placeholders, not user input
	query := fmt.Sprintf(`
		SELECT
		    i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		    i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		    i.created_at, i.created_by, i.updated_at, i.closed_at, i.external_ref, i.source_repo,
		    COALESCE(COUNT(d.depends_on_id), 0) as blocked_by_count,
		    COALESCE(GROUP_CONCAT(d.depends_on_id, ','), '') as blocker_ids
		FROM issues i
		LEFT JOIN dependencies d ON i.id = d.issue_id
		    AND d.type = 'blocks'
		    AND (
		        -- Local blockers: must be open/in_progress/blocked/deferred
		        EXISTS (
		            SELECT 1 FROM issues blocker
		            WHERE blocker.id = d.depends_on_id
		            AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		        )
		        -- External refs: always included (resolution happens at query time)
		        OR d.depends_on_id LIKE 'external:%%'
		    )
		WHERE i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  AND i.pinned = 0
		  AND (
		      i.status = 'blocked'
		      OR i.status = 'deferred'
		      -- Has local open blockers
		      OR EXISTS (
		          SELECT 1 FROM dependencies d2
		          JOIN issues blocker ON d2.depends_on_id = blocker.id
		          WHERE d2.issue_id = i.id
		            AND d2.type = 'blocks'
		            AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		      )
		      -- Has external blockers (always considered blocking until resolved)
		      OR EXISTS (
		          SELECT 1 FROM dependencies d3
		          WHERE d3.issue_id = i.id
		            AND d3.type = 'blocks'
		            AND d3.depends_on_id LIKE 'external:%%'
		      )
		  )
		  %s
		GROUP BY i.id
		ORDER BY i.priority ASC
	`, filterSQL)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocked issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var blocked []*types.BlockedIssue
	for rows.Next() {
		var issue types.BlockedIssue
		var createdAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
		var updatedAtStr sql.NullString // TEXT column - must parse manually for cross-driver compatibility
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
			&createdAtStr, &issue.CreatedBy, &updatedAtStr, &closedAt, &externalRef, &sourceRepo, &issue.BlockedByCount,
			&blockerIDsStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan blocked issue: %w", err)
		}

		// Parse timestamp strings (TEXT columns require manual parsing)
		if createdAtStr.Valid {
			issue.CreatedAt = parseTimeString(createdAtStr.String)
		}
		if updatedAtStr.Valid {
			issue.UpdatedAt = parseTimeString(updatedAtStr.String)
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

	// Filter out satisfied external dependencies from BlockedBy lists
	// Only check if external_projects are configured
	if len(config.GetExternalProjects()) > 0 && len(blocked) > 0 {
		blocked = filterBlockedByExternalDeps(ctx, blocked)
	}

	return blocked, nil
}

// filterBlockedByExternalDeps removes satisfied external deps from BlockedBy lists.
// Issues with no remaining blockers are removed unless they have status=blocked/deferred.
func filterBlockedByExternalDeps(ctx context.Context, blocked []*types.BlockedIssue) []*types.BlockedIssue {
	if len(blocked) == 0 {
		return blocked
	}

	// Collect all unique external refs across all blocked issues
	externalRefs := make(map[string]bool)
	for _, issue := range blocked {
		for _, ref := range issue.BlockedBy {
			if strings.HasPrefix(ref, "external:") {
				externalRefs[ref] = true
			}
		}
	}

	// If no external refs, return as-is
	if len(externalRefs) == 0 {
		return blocked
	}

	// Check all external refs in batch
	refList := make([]string, 0, len(externalRefs))
	for ref := range externalRefs {
		refList = append(refList, ref)
	}
	statuses := CheckExternalDeps(ctx, refList)

	// Build set of satisfied refs
	satisfiedRefs := make(map[string]bool)
	for ref, status := range statuses {
		if status.Satisfied {
			satisfiedRefs[ref] = true
		}
	}

	// If nothing is satisfied, return as-is
	if len(satisfiedRefs) == 0 {
		return blocked
	}

	// Filter each issue's BlockedBy list
	result := make([]*types.BlockedIssue, 0, len(blocked))
	for _, issue := range blocked {
		// Filter out satisfied external deps
		var filteredBlockers []string
		for _, ref := range issue.BlockedBy {
			if !satisfiedRefs[ref] {
				filteredBlockers = append(filteredBlockers, ref)
			}
		}

		// Update issue with filtered blockers
		issue.BlockedBy = filteredBlockers
		issue.BlockedByCount = len(filteredBlockers)

		// Keep issue if it has remaining blockers OR has blocked/deferred status
		// (status=blocked/deferred issues always show even with no dep blockers)
		if len(filteredBlockers) > 0 || issue.Status == "blocked" || issue.Status == "deferred" {
			result = append(result, issue)
		}
	}

	return result
}

// IsBlocked checks if an issue is blocked by open dependencies (GH#962).
// Returns true if the issue is in the blocked_issues_cache, along with a list
// of issue IDs that are blocking it.
// This is used to prevent closing issues that still have open blockers.
func (s *SQLiteStorage) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	// First check if the issue is in the blocked cache
	var inCache bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM blocked_issues_cache WHERE issue_id = ?)
	`, issueID).Scan(&inCache)
	if err != nil {
		return false, nil, fmt.Errorf("failed to check blocked status: %w", err)
	}

	if !inCache {
		return false, nil, nil
	}

	// Get the blocking issue IDs
	// We query dependencies for 'blocks' type where the blocker is still open
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.depends_on_id
		FROM dependencies d
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE d.issue_id = ?
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		ORDER BY blocker.priority ASC
	`, issueID)
	if err != nil {
		return true, nil, fmt.Errorf("failed to get blockers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var blockers []string
	for rows.Next() {
		var blockerID string
		if err := rows.Scan(&blockerID); err != nil {
			return true, nil, fmt.Errorf("failed to scan blocker ID: %w", err)
		}
		blockers = append(blockers, blockerID)
	}

	return true, blockers, rows.Err()
}

// GetNewlyUnblockedByClose returns issues that became unblocked when the given issue was closed.
// This is used by the --suggest-next flag on bd close to show what work is now available.
// An issue is "newly unblocked" if:
//   - It had a 'blocks' dependency on the closed issue
//   - It is now unblocked (not in blocked_issues_cache)
//   - It has status open or in_progress (ready to work on)
//
// The cache is already rebuilt by CloseIssue before this is called, so we just need to
// find dependents that are no longer blocked.
func (s *SQLiteStorage) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	// Find issues that:
	// 1. Had a 'blocks' dependency on the closed issue
	// 2. Are now NOT in blocked_issues_cache (unblocked)
	// 3. Have status open or in_progress
	// 4. Are not pinned
	query := `
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.created_by, i.owner, i.updated_at, i.closed_at, i.external_ref, i.source_repo, i.close_reason,
		       i.deleted_at, i.deleted_by, i.delete_reason, i.original_type,
		       i.sender, i.ephemeral, i.pinned, i.is_template, i.crystallizes,
		       i.await_type, i.await_id, i.timeout_ns, i.waiters,
		       i.hook_bead, i.role_bead, i.agent_state, i.last_activity, i.role_type, i.rig, i.mol_type,
		       i.due_at, i.defer_until
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		WHERE d.depends_on_id = ?
		  AND d.type = 'blocks'
		  AND i.status IN ('open', 'in_progress')
		  AND i.pinned = 0
		  AND NOT EXISTS (
		      SELECT 1 FROM blocked_issues_cache WHERE issue_id = i.id
		  )
		ORDER BY i.priority ASC
	`

	rows, err := s.db.QueryContext(ctx, query, closedIssueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get newly unblocked issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
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

// ExcludeIDPatternsConfigKey is the config key for ID exclusion patterns in GetReadyWork
const ExcludeIDPatternsConfigKey = "ready.exclude_id_patterns"

// DefaultExcludeIDPatterns are the default patterns to exclude from GetReadyWork
// These exclude molecule steps (-mol-) and wisps (-wisp-) which are internal workflow items
var DefaultExcludeIDPatterns = []string{"-mol-", "-wisp-"}

// getExcludeIDPatterns returns the ID patterns to exclude from GetReadyWork.
// Reads from ready.exclude_id_patterns config, defaults to DefaultExcludeIDPatterns.
// Config format: comma-separated patterns, e.g., "-mol-,-wisp-"
func (s *SQLiteStorage) getExcludeIDPatterns(ctx context.Context) []string {
	value, err := s.GetConfig(ctx, ExcludeIDPatternsConfigKey)
	if err != nil || value == "" {
		return DefaultExcludeIDPatterns
	}

	// Parse comma-separated patterns
	parts := strings.Split(value, ",")
	patterns := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			patterns = append(patterns, p)
		}
	}

	if len(patterns) == 0 {
		return DefaultExcludeIDPatterns
	}
	return patterns
}
