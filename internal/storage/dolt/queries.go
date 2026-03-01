package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// SearchIssues finds issues matching query and filters
func (s *DoltStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// Route ephemeral-only queries to wisps table, falling through to
	// issues table if wisps table doesn't exist (pre-migration databases).
	if filter.Ephemeral != nil && *filter.Ephemeral {
		if results, err := s.searchWisps(ctx, query, filter); err == nil && len(results) > 0 {
			return results, nil
		}
		// Fall through: query issues table with ephemeral filter instead
	}

	// If searching by IDs that are all ephemeral, try wisps table first,
	// falling through to the issues table if not found (handles pre-migration
	// databases where ephemeral rows live in issues with ephemeral=1).
	if len(filter.IDs) > 0 && allEphemeral(filter.IDs) {
		if results, err := s.searchWisps(ctx, query, filter); err == nil && len(results) > 0 {
			return results, nil
		}
		// Fall through: wisps table may not exist or IDs may be in issues table
	}

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
	// Also includes dotted-ID children (e.g., "parent.1.2" is child of "parent"),
	// but only if they haven't been explicitly reparented via dependencies.
	// An explicit parent-child dependency takes precedence over dotted-ID prefix.
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR (id LIKE CONCAT(?, '.%') AND id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child')))")
		args = append(args, parentID, parentID)
	}

	// No-parent filtering: exclude issues that are children of another issue
	if filter.NoParent {
		whereClauses = append(whereClauses, "id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child')")
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

	// Metadata existence check (GH#1406)
	if filter.HasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(filter.HasMetadataKey); err != nil {
			return nil, err
		}
		whereClauses = append(whereClauses, "JSON_EXTRACT(metadata, ?) IS NOT NULL")
		args = append(args, "$."+filter.HasMetadataKey)
	}

	// Metadata field equality filters (GH#1406)
	// Sort keys for deterministic query generation (important for testing)
	if len(filter.MetadataFields) > 0 {
		metaKeys := make([]string, 0, len(filter.MetadataFields))
		for k := range filter.MetadataFields {
			metaKeys = append(metaKeys, k)
		}
		sort.Strings(metaKeys)
		for _, k := range metaKeys {
			if err := storage.ValidateMetadataKey(k); err != nil {
				return nil, err
			}
			whereClauses = append(whereClauses, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?")
			args = append(args, "$."+k, filter.MetadataFields[k])
		}
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

	doltResults, err := s.scanIssueIDs(ctx, rows)
	if err != nil {
		return nil, err
	}

	// When filter.Ephemeral is nil (search everything), also search the wisps
	// table and merge results. This ensures ephemeral beads appear in queries.
	if filter.Ephemeral == nil {
		wispResults, wispErr := s.searchWisps(ctx, query, filter)
		if wispErr == nil && len(wispResults) > 0 {
			// Deduplicate by ID (prefer Dolt version if exists in both)
			seen := make(map[string]bool, len(doltResults))
			for _, issue := range doltResults {
				seen[issue.ID] = true
			}
			for _, issue := range wispResults {
				if !seen[issue.ID] {
					doltResults = append(doltResults, issue)
				}
			}
		}
	}

	return doltResults, nil
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
	whereClauses := []string{
		statusClause,
		"(pinned = 0 OR pinned IS NULL)", // Exclude pinned issues (context markers, not work)
	}
	if !filter.IncludeEphemeral {
		whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
	}
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
	} else {
		// Exclude workflow/identity types from ready work by default.
		// These are internal items, not actionable work for agents to claim:
		// - merge-request: processed by Refinery
		// - gate: async wait conditions
		// - molecule: workflow containers
		// - message: mail/communication items
		// - agent: identity/state tracking beads
		// - role: agent role definitions (reference metadata)
		// - rig: rig identity beads (reference metadata)
		excludeTypes := []string{"merge-request", "gate", "molecule", "message", "agent", "role", "rig"}
		placeholders := make([]string, len(excludeTypes))
		for i, t := range excludeTypes {
			placeholders[i] = "?"
			args = append(args, t)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT id FROM issues WHERE issue_type NOT IN (%s))", strings.Join(placeholders, ",")))
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
	// Exclude children of future-deferred parents (GH#1190)
	if !filter.IncludeDeferred {
		whereClauses = append(whereClauses, `
			NOT EXISTS (
				SELECT 1 FROM dependencies d_parent
				JOIN issues parent ON parent.id = d_parent.depends_on_id
				WHERE d_parent.issue_id = issues.id
				  AND d_parent.type = 'parent-child'
				  AND parent.defer_until IS NOT NULL
				  AND parent.defer_until > NOW()
			)
		`)
	}
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}
	// Parent filtering: filter to children of specified parent (GH#2009)
	// Explicit parent-child dependency takes precedence over dotted-ID prefix.
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR (id LIKE CONCAT(?, '.%') AND id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child')))")
		args = append(args, parentID, parentID)
	}

	// Metadata existence check (GH#1406)
	if filter.HasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(filter.HasMetadataKey); err != nil {
			return nil, err
		}
		whereClauses = append(whereClauses, "JSON_EXTRACT(metadata, ?) IS NOT NULL")
		args = append(args, "$."+filter.HasMetadataKey)
	}

	// Metadata field equality filters (GH#1406)
	if len(filter.MetadataFields) > 0 {
		metaKeys := make([]string, 0, len(filter.MetadataFields))
		for k := range filter.MetadataFields {
			metaKeys = append(metaKeys, k)
		}
		sort.Strings(metaKeys)
		for _, k := range metaKeys {
			if err := storage.ValidateMetadataKey(k); err != nil {
				return nil, err
			}
			whereClauses = append(whereClauses, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?")
			args = append(args, "$."+k, filter.MetadataFields[k])
		}
	}

	// Exclude blocked issues: pre-compute blocked set using separate single-table
	// queries to avoid Dolt's joinIter panic (join_iters.go:192).
	// Correlated EXISTS/NOT EXISTS subqueries across tables trigger the same panic.
	blockedIDs, err := s.computeBlockedIDs(ctx)
	if err == nil && len(blockedIDs) > 0 {
		// Also exclude children of blocked parents (GH#1495):
		// If a parent/epic is blocked, its children should not appear as ready work.
		childrenOfBlocked, childErr := s.getChildrenOfIssues(ctx, blockedIDs)
		if childErr == nil {
			for _, childID := range childrenOfBlocked {
				blockedIDs = append(blockedIDs, childID)
			}
		}

		placeholders := make([]string, len(blockedIDs))
		for i, id := range blockedIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (%s)", strings.Join(placeholders, ", ")))
	}

	whereSQL := "WHERE " + strings.Join(whereClauses, " AND ")

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	// Build ORDER BY clause based on SortPolicy
	var orderBySQL string
	switch filter.SortPolicy {
	case types.SortPolicyOldest:
		orderBySQL = "ORDER BY created_at ASC"
	case types.SortPolicyPriority:
		orderBySQL = "ORDER BY priority ASC, created_at DESC"
	case types.SortPolicyHybrid, "": // hybrid is the default
		// Recent issues (created within 48 hours) are sorted by priority;
		// older issues are sorted by age (oldest first) to prevent starvation.
		orderBySQL = `ORDER BY
			CASE WHEN created_at >= DATE_SUB(NOW(), INTERVAL 48 HOUR) THEN 0 ELSE 1 END ASC,
			CASE WHEN created_at >= DATE_SUB(NOW(), INTERVAL 48 HOUR) THEN priority ELSE 999 END ASC,
			created_at ASC`
	default:
		orderBySQL = "ORDER BY priority ASC, created_at DESC"
	}

	// nolint:gosec // G201: whereSQL contains column comparisons with ?, limitSQL is a safe integer
	query := fmt.Sprintf(`
		SELECT id FROM issues
		%s
		%s
		%s
	`, whereSQL, orderBySQL, limitSQL)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready work: %w", err)
	}
	defer rows.Close()

	issues, err := s.scanIssueIDs(ctx, rows)
	if err != nil {
		return nil, err
	}

	// When IncludeEphemeral is set, also query the wisps table for ready work.
	if filter.IncludeEphemeral {
		wispFilter := types.IssueFilter{Limit: filter.Limit}
		if filter.Status != "" {
			s := filter.Status
			wispFilter.Status = &s
		}
		wisps, wErr := s.searchWisps(ctx, "", wispFilter)
		if wErr == nil {
			issues = append(issues, wisps...)
		}
	}

	return issues, nil
}

// GetBlockedIssues returns issues that are blocked by other issues.
// Uses separate single-table queries with Go-level filtering to avoid
// correlated EXISTS subqueries that trigger Dolt's joinIter panic
// (slice bounds out of range at join_iters.go:192).
// Same fix pattern as GetStatistics blocked count (fc16065c, a4a21958).
func (s *DoltStore) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Step 1: Get all open/active issue IDs into a set (scan both tables — bd-w2w)
	activeIDs := make(map[string]bool)
	for _, table := range []string{"issues", "wisps"} {
		//nolint:gosec // G201: table is hardcoded to "issues" or "wisps"
		activeRows, err := s.queryContext(ctx, fmt.Sprintf(`
			SELECT id FROM %s
			WHERE status NOT IN ('closed', 'pinned')
		`, table))
		if err != nil {
			return nil, fmt.Errorf("failed to get active issues from %s: %w", table, err)
		}
		for activeRows.Next() {
			var id string
			if err := activeRows.Scan(&id); err != nil {
				_ = activeRows.Close() // Best effort cleanup on error path
				return nil, wrapScanError("get blocked issues: scan active ID", err)
			}
			activeIDs[id] = true
		}
		_ = activeRows.Close() // Redundant close for safety (rows already iterated)
		if err := activeRows.Err(); err != nil {
			return nil, wrapQueryError("get blocked issues: active rows", err)
		}
	}

	// Step 2: Get canonical blocked set via computeBlockedIDs, which handles
	// both 'blocks' and 'waits-for' dependencies with full gate evaluation.
	blockedIDList, err := s.computeBlockedIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to compute blocked IDs: %w", err)
	}
	blockedSet := make(map[string]bool, len(blockedIDList))
	for _, id := range blockedIDList {
		blockedSet[id] = true
	}

	// Step 3: Get blocking + waits-for + conditional-blocks deps to build BlockedBy lists
	// Scan both dependencies and wisp_dependencies tables (bd-w2w)
	blockerMap := make(map[string][]string)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
		depRows, err := s.queryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, depends_on_id FROM %s
			WHERE type IN ('blocks', 'waits-for', 'conditional-blocks')
		`, depTable))
		if err != nil {
			return nil, fmt.Errorf("failed to get blocking dependencies from %s: %w", depTable, err)
		}

		for depRows.Next() {
			var issueID, blockerID string
			if err := depRows.Scan(&issueID, &blockerID); err != nil {
				_ = depRows.Close() // Best effort cleanup on error path
				return nil, wrapScanError("get blocked issues: scan dependency", err)
			}
			// Only include if computeBlockedIDs confirmed this issue is blocked
			if blockedSet[issueID] && activeIDs[blockerID] {
				blockerMap[issueID] = append(blockerMap[issueID], blockerID)
			}
		}
		_ = depRows.Close() // Redundant close for safety (rows already iterated)
		if err := depRows.Err(); err != nil {
			return nil, wrapQueryError("get blocked issues: dependency rows", err)
		}
	}

	// Step 4: Batch-fetch all blocked issues and build results
	blockedIDs := make([]string, 0, len(blockerMap))
	for id := range blockerMap {
		blockedIDs = append(blockedIDs, id)
	}
	issues, err := s.GetIssuesByIDs(ctx, blockedIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to batch-fetch blocked issues: %w", err)
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	// Parent filtering: restrict to children of specified parent (GH#2009)
	var parentChildSet map[string]bool
	if filter.ParentID != nil {
		parentChildSet = make(map[string]bool)
		parentID := *filter.ParentID
		children, childErr := s.getChildrenOfIssues(ctx, []string{parentID})
		if childErr == nil {
			for _, childID := range children {
				parentChildSet[childID] = true
			}
		}
		// Also include dotted-ID children (e.g., "parent.1.2")
		for id := range blockerMap {
			if strings.HasPrefix(id, parentID+".") {
				parentChildSet[id] = true
			}
		}
	}

	var results []*types.BlockedIssue
	for id, blockerIDs := range blockerMap {
		// Skip issues not under requested parent (GH#2009)
		if parentChildSet != nil && !parentChildSet[id] {
			continue
		}

		issue, ok := issueMap[id]
		if !ok || issue == nil {
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
	// Use separate single-table queries to avoid Dolt's joinIter panic
	// (join_iters.go:192) which triggers on multi-table JOINs.

	// Step 1: Get open epic IDs (single-table scan)
	epicRows, err := s.queryContext(ctx, `
		SELECT id FROM issues
		WHERE issue_type = 'epic'
		  AND status != 'closed'
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get epics: %w", err)
	}
	var epicIDs []string
	for epicRows.Next() {
		var id string
		if err := epicRows.Scan(&id); err != nil {
			_ = epicRows.Close() // Best effort cleanup on error path
			return nil, wrapScanError("get epics eligible for closure", err)
		}
		epicIDs = append(epicIDs, id)
	}
	_ = epicRows.Close() // Redundant close for safety (rows already iterated)

	if len(epicIDs) == 0 {
		return nil, nil
	}

	// Step 2: Get parent-child dependencies (single-table scan)
	depRows, err := s.queryContext(ctx, `
		SELECT depends_on_id, issue_id FROM dependencies
		WHERE type = 'parent-child'
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get parent-child deps: %w", err)
	}
	// Map: parent_id -> list of child IDs
	epicChildMap := make(map[string][]string)
	epicSet := make(map[string]bool, len(epicIDs))
	for _, id := range epicIDs {
		epicSet[id] = true
	}
	for depRows.Next() {
		var parentID, childID string
		if err := depRows.Scan(&parentID, &childID); err != nil {
			_ = depRows.Close() // Best effort cleanup on error path
			return nil, wrapScanError("get epics: scan parent-child dep", err)
		}
		if epicSet[parentID] {
			epicChildMap[parentID] = append(epicChildMap[parentID], childID)
		}
	}
	_ = depRows.Close() // Redundant close for safety (rows already iterated)

	// Step 3: Batch-fetch statuses for all child issues across all epics
	allChildIDs := make([]string, 0)
	for _, children := range epicChildMap {
		allChildIDs = append(allChildIDs, children...)
	}
	childStatusMap := make(map[string]string)
	if len(allChildIDs) > 0 {
		placeholders := make([]string, len(allChildIDs))
		args := make([]interface{}, len(allChildIDs))
		for i, id := range allChildIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		// Check both issues and wisps tables for child statuses (bd-w2w)
		for _, table := range []string{"issues", "wisps"} {
			// nolint:gosec // G201: table is hardcoded, placeholders contains only ? markers
			statusQuery := fmt.Sprintf("SELECT id, status FROM %s WHERE id IN (%s)", table, strings.Join(placeholders, ","))
			statusRows, err := s.queryContext(ctx, statusQuery, args...)
			if err != nil {
				return nil, fmt.Errorf("failed to batch-fetch child statuses from %s: %w", table, err)
			}
			for statusRows.Next() {
				var id, status string
				if err := statusRows.Scan(&id, &status); err != nil {
					_ = statusRows.Close()
					return nil, wrapScanError("get epics: scan child status", err)
				}
				childStatusMap[id] = status
			}
			_ = statusRows.Close()
		}
	}

	// Step 4: Batch-fetch all epic issues
	epicsWithChildren := make([]string, 0)
	for _, epicID := range epicIDs {
		if len(epicChildMap[epicID]) > 0 {
			epicsWithChildren = append(epicsWithChildren, epicID)
		}
	}
	epicIssues, err := s.GetIssuesByIDs(ctx, epicsWithChildren)
	if err != nil {
		return nil, fmt.Errorf("failed to batch-fetch epic issues: %w", err)
	}
	epicIssueMap := make(map[string]*types.Issue, len(epicIssues))
	for _, issue := range epicIssues {
		epicIssueMap[issue.ID] = issue
	}

	// Step 5: Build results from cached data
	var results []*types.EpicStatus
	for _, epicID := range epicIDs {
		children := epicChildMap[epicID]
		if len(children) == 0 {
			continue
		}

		issue, ok := epicIssueMap[epicID]
		if !ok || issue == nil {
			continue
		}

		totalChildren := len(children)
		closedChildren := 0
		for _, childID := range children {
			if status, ok := childStatusMap[childID]; ok && types.Status(status) == types.StatusClosed {
				closedChildren++
			}
		}

		results = append(results, &types.EpicStatus{
			Epic:             issue,
			TotalChildren:    totalChildren,
			ClosedChildren:   closedChildren,
			EligibleForClose: totalChildren > 0 && totalChildren == closedChildren,
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

	// Get counts per status.
	// Important: COALESCE to avoid NULL scans when the table is empty.
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0) as open_count,
			COALESCE(SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END), 0) as in_progress,
			COALESCE(SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END), 0) as closed,
			COALESCE(SUM(CASE WHEN status = 'deferred' THEN 1 ELSE 0 END), 0) as deferred,
			COALESCE(SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END), 0) as pinned
		FROM issues
	`).Scan(
		&stats.TotalIssues,
		&stats.OpenIssues,
		&stats.InProgressIssues,
		&stats.ClosedIssues,
		&stats.DeferredIssues,
		&stats.PinnedIssues,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get statistics: %w", err)
	}

	// Blocked count: reuse computeBlockedIDs which caches the result across
	// GetReadyWork and GetStatistics calls within the same CLI invocation.
	var blockedCount int
	blockedIDs, err := s.computeBlockedIDs(ctx)
	if err == nil {
		blockedCount = len(blockedIDs)
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

// computeBlockedIDs returns the set of issue IDs that are blocked by active issues.
// Uses separate single-table queries with Go-level filtering to avoid Dolt's
// joinIter panic (slice bounds out of range at join_iters.go:192).
// Results are cached per DoltStore lifetime and invalidated when dependencies
// change (AddDependency, RemoveDependency).
// Caller must hold s.mu (at least RLock).
func (s *DoltStore) computeBlockedIDs(ctx context.Context) ([]string, error) {
	s.cacheMu.Lock()
	if s.blockedIDsCached {
		result := s.blockedIDsCache
		s.cacheMu.Unlock()
		return result, nil
	}
	s.cacheMu.Unlock()

	// Step 1: Get all active issue IDs (scan both tables — bd-w2w)
	activeIDs := make(map[string]bool)
	for _, table := range []string{"issues", "wisps"} {
		//nolint:gosec // G201: table is hardcoded to "issues" or "wisps"
		activeRows, err := s.queryContext(ctx, fmt.Sprintf(`
			SELECT id FROM %s
			WHERE status NOT IN ('closed', 'pinned')
		`, table))
		if err != nil {
			return nil, wrapQueryError("compute blocked IDs: get active issues from "+table, err)
		}
		for activeRows.Next() {
			var id string
			if err := activeRows.Scan(&id); err != nil {
				_ = activeRows.Close() // Best effort cleanup on error path
				return nil, wrapScanError("compute blocked IDs: scan active issue", err)
			}
			activeIDs[id] = true
		}
		_ = activeRows.Close() // Redundant close for safety (rows already iterated)
		if err := activeRows.Err(); err != nil {
			return nil, wrapQueryError("compute blocked IDs: active rows", err)
		}
	}

	// Step 2: Get blocking deps, waits-for gates, and conditional-blocks
	// Scan both dependencies and wisp_dependencies tables (bd-w2w)
	type depRecord struct {
		issueID, dependsOnID, depType string
		metadata                      sql.NullString
	}
	var allDeps []depRecord
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
		depRows, err := s.queryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, depends_on_id, type, metadata FROM %s
			WHERE type IN ('blocks', 'waits-for', 'conditional-blocks')
		`, depTable))
		if err != nil {
			return nil, wrapQueryError("compute blocked IDs: get dependencies from "+depTable, err)
		}
		for depRows.Next() {
			var rec depRecord
			if err := depRows.Scan(&rec.issueID, &rec.dependsOnID, &rec.depType, &rec.metadata); err != nil {
				_ = depRows.Close()
				return nil, wrapScanError("compute blocked IDs: scan dependency", err)
			}
			allDeps = append(allDeps, rec)
		}
		_ = depRows.Close()
		if err := depRows.Err(); err != nil {
			return nil, wrapQueryError("compute blocked IDs: dependency rows from "+depTable, err)
		}
	}

	type waitsForDep struct {
		issueID   string
		spawnerID string
		gate      string
	}
	var waitsForDeps []waitsForDep
	needsClosedChildren := false

	// Step 3: Filter direct blockers in Go; collect waits-for edges
	blockedSet := make(map[string]bool)
	for _, rec := range allDeps {
		switch rec.depType {
		case string(types.DepBlocks), string(types.DepConditionalBlocks):
			// Both blocks and conditional-blocks gate readiness while the
			// blocker is active. For conditional-blocks ("B runs only if A
			// fails"), B cannot be ready while A's outcome is still unknown.
			if activeIDs[rec.issueID] && activeIDs[rec.dependsOnID] {
				blockedSet[rec.issueID] = true
			}
		case string(types.DepWaitsFor):
			// waits-for only matters for active gate issues
			if !activeIDs[rec.issueID] {
				continue
			}
			gate := types.ParseWaitsForGateMetadata(rec.metadata.String)
			if gate == types.WaitsForAnyChildren {
				needsClosedChildren = true
			}
			waitsForDeps = append(waitsForDeps, waitsForDep{
				issueID: rec.issueID,
				// depends_on_id is the canonical spawner ID for waits-for edges.
				// metadata.spawner_id is parsed for compatibility but not required here.
				spawnerID: rec.dependsOnID,
				gate:      gate,
			})
		}
	}

	if len(waitsForDeps) > 0 {
		// Step 4: Load direct children for each waits-for spawner.
		// Scan both dependencies and wisp_dependencies tables (bd-w2w)
		spawnerIDs := make(map[string]struct{})
		for _, dep := range waitsForDeps {
			spawnerIDs[dep.spawnerID] = struct{}{}
		}

		placeholders := make([]string, 0, len(spawnerIDs))
		args := make([]interface{}, 0, len(spawnerIDs))
		for spawnerID := range spawnerIDs {
			placeholders = append(placeholders, "?")
			args = append(args, spawnerID)
		}

		spawnerChildren := make(map[string][]string)
		childIDs := make(map[string]struct{})
		for _, depTbl := range []string{"dependencies", "wisp_dependencies"} {
			// nolint:gosec // G201: depTbl is hardcoded, placeholders are generated values
			childQuery := fmt.Sprintf(`
				SELECT issue_id, depends_on_id FROM %s
				WHERE type = 'parent-child' AND depends_on_id IN (%s)
			`, depTbl, strings.Join(placeholders, ","))
			childRows, err := s.queryContext(ctx, childQuery, args...)
			if err != nil {
				return nil, wrapQueryError("compute blocked IDs: get spawner children from "+depTbl, err)
			}

			for childRows.Next() {
				var childID, parentID string
				if err := childRows.Scan(&childID, &parentID); err != nil {
					_ = childRows.Close() // Best effort cleanup on error path
					return nil, wrapScanError("compute blocked IDs: scan child", err)
				}
				spawnerChildren[parentID] = append(spawnerChildren[parentID], childID)
				childIDs[childID] = struct{}{}
			}
			_ = childRows.Close()
			if err := childRows.Err(); err != nil {
				return nil, wrapQueryError("compute blocked IDs: child rows from "+depTbl, err)
			}
		}

		closedChildren := make(map[string]bool)
		if needsClosedChildren && len(childIDs) > 0 {
			childPlaceholders := make([]string, 0, len(childIDs))
			childArgs := make([]interface{}, 0, len(childIDs))
			for childID := range childIDs {
				childPlaceholders = append(childPlaceholders, "?")
				childArgs = append(childArgs, childID)
			}

			// Check closed status in both issues and wisps tables (bd-w2w)
			for _, issueTbl := range []string{"issues", "wisps"} {
				// nolint:gosec // G201: issueTbl is hardcoded, placeholders are generated values
				closedQuery := fmt.Sprintf(`
					SELECT id FROM %s
					WHERE status = 'closed' AND id IN (%s)
				`, issueTbl, strings.Join(childPlaceholders, ","))
				closedRows, err := s.queryContext(ctx, closedQuery, childArgs...)
				if err != nil {
					return nil, wrapQueryError("compute blocked IDs: get closed children from "+issueTbl, err)
				}
				for closedRows.Next() {
					var childID string
					if err := closedRows.Scan(&childID); err != nil {
						_ = closedRows.Close() // Best effort cleanup on error path
						return nil, wrapScanError("compute blocked IDs: scan closed child", err)
					}
					closedChildren[childID] = true
				}
				_ = closedRows.Close()
				if err := closedRows.Err(); err != nil {
					return nil, wrapQueryError("compute blocked IDs: closed child rows from "+issueTbl, err)
				}
			}
		}

		// Step 5: Evaluate waits-for gates against current child states.
		for _, dep := range waitsForDeps {
			children := spawnerChildren[dep.spawnerID]
			switch dep.gate {
			case types.WaitsForAnyChildren:
				// Block only while spawned children are active and none have completed.
				if len(children) == 0 {
					continue
				}
				hasClosedChild := false
				hasActiveChild := false
				for _, childID := range children {
					if closedChildren[childID] {
						hasClosedChild = true
						break
					}
					if activeIDs[childID] {
						hasActiveChild = true
					}
				}
				if !hasClosedChild && hasActiveChild {
					blockedSet[dep.issueID] = true
				}
			default:
				// all-children / children-of(step): block while any child remains active.
				for _, childID := range children {
					if activeIDs[childID] {
						blockedSet[dep.issueID] = true
						break
					}
				}
			}
		}
	}

	result := make([]string, 0, len(blockedSet))
	for id := range blockedSet {
		result = append(result, id)
	}

	s.cacheMu.Lock()
	s.blockedIDsCache = result
	s.blockedIDsCacheMap = blockedSet
	s.blockedIDsCached = true
	s.cacheMu.Unlock()

	return result, nil
}

// invalidateBlockedIDsCache clears the blocked IDs cache so the next call
// to computeBlockedIDs will recompute from the database.
func (s *DoltStore) invalidateBlockedIDsCache() {
	s.cacheMu.Lock()
	s.blockedIDsCached = false
	s.blockedIDsCache = nil
	s.blockedIDsCacheMap = nil
	s.cacheMu.Unlock()
}

// getChildrenOfIssues returns IDs of direct children (parent-child deps) of the given issue IDs.
// Used to propagate blocked status from parents to children (GH#1495).
func (s *DoltStore) getChildrenOfIssues(ctx context.Context, parentIDs []string) ([]string, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(parentIDs))
	args := make([]interface{}, len(parentIDs))
	for i, id := range parentIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	// nolint:gosec // G201: placeholders are generated values, data passed via args
	query := fmt.Sprintf(`
		SELECT issue_id FROM dependencies
		WHERE type = 'parent-child' AND depends_on_id IN (%s)
	`, strings.Join(placeholders, ","))
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapQueryError("get children of issues", err)
	}
	defer rows.Close()

	var children []string
	for rows.Next() {
		var childID string
		if err := rows.Scan(&childID); err != nil {
			return nil, wrapScanError("get children of issues", err)
		}
		children = append(children, childID)
	}
	return children, rows.Err()
}

// GetMoleculeProgress returns progress stats for a molecule
func (s *DoltStore) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	stats := &types.MoleculeProgressStats{
		MoleculeID: moleculeID,
	}

	// Route to correct table based on whether molecule is a wisp (bd-w2w)
	issueTable := "issues"
	depTable := "dependencies"
	if s.isActiveWisp(ctx, moleculeID) {
		issueTable = "wisps"
		depTable = "wisp_dependencies"
	}

	// Get molecule title
	var title sql.NullString
	//nolint:gosec // G201: issueTable is hardcoded to "issues" or "wisps"
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT title FROM %s WHERE id = ?", issueTable), moleculeID).Scan(&title)
	if err == nil && title.Valid {
		stats.MoleculeTitle = title.String
	}

	// Use separate single-table queries to avoid Dolt's joinIter panic
	// (join_iters.go:192) which triggers on JOIN between issues and dependencies.

	// Step 1: Get child issue IDs from dependencies table (single-table scan)
	//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
	depRows, err := s.queryContext(ctx, fmt.Sprintf(`
		SELECT issue_id FROM %s
		WHERE depends_on_id = ? AND type = 'parent-child'
	`, depTable), moleculeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get molecule children: %w", err)
	}
	var childIDs []string
	for depRows.Next() {
		var id string
		if err := depRows.Scan(&id); err != nil {
			_ = depRows.Close() // Best effort cleanup on error path
			return nil, wrapScanError("get molecule progress: scan child", err)
		}
		childIDs = append(childIDs, id)
	}
	_ = depRows.Close() // Redundant close for safety (rows already iterated)

	// Step 2: Batch-fetch status for all children (single batched query)
	// Children of a wisp molecule are also wisps, so use the same table.
	if len(childIDs) > 0 {
		placeholders := make([]string, len(childIDs))
		args := make([]interface{}, len(childIDs))
		for i, id := range childIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		// nolint:gosec // G201: issueTable is hardcoded, placeholders contains only ? markers
		query := fmt.Sprintf("SELECT id, status FROM %s WHERE id IN (%s)", issueTable, strings.Join(placeholders, ","))
		statusRows, err := s.queryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to batch-fetch child statuses: %w", err)
		}
		type childInfo struct {
			status string
		}
		childMap := make(map[string]childInfo)
		for statusRows.Next() {
			var id, status string
			if err := statusRows.Scan(&id, &status); err != nil {
				_ = statusRows.Close()
				return nil, wrapScanError("get molecule progress: scan status", err)
			}
			childMap[id] = childInfo{status: status}
		}
		_ = statusRows.Close()

		for _, childID := range childIDs {
			info, ok := childMap[childID]
			if !ok {
				continue
			}
			stats.Total++
			switch types.Status(info.status) {
			case types.StatusClosed:
				stats.Completed++
			case types.StatusInProgress:
				stats.InProgress++
				if stats.CurrentStepID == "" {
					stats.CurrentStepID = childID
				}
			}
		}
	}

	return stats, nil
}

// GetNextChildID returns the next available child ID for a parent
func (s *DoltStore) GetNextChildID(ctx context.Context, parentID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", wrapTransactionError("get next child ID: begin", err)
	}
	defer tx.Rollback()

	var lastChild int
	err = tx.QueryRowContext(ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", parentID).Scan(&lastChild)
	if err == sql.ErrNoRows {
		lastChild = 0
	} else if err != nil {
		return "", wrapQueryError("get next child ID: read counter", err)
	}

	// Check existing children to prevent overwrites after JSONL import (GH#2166).
	// The counter may be stale if issues were imported without reconciling child_counters.
	var maxExisting sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT MAX(CAST(SUBSTRING_INDEX(id, '.', -1) AS UNSIGNED))
		FROM issues
		WHERE id LIKE CONCAT(?, '.%')
		  AND id NOT LIKE CONCAT(?, '.%.%')
	`, parentID, parentID).Scan(&maxExisting)
	if err != nil {
		return "", wrapQueryError("get next child ID: scan existing children", err)
	}
	if maxExisting.Valid && int(maxExisting.Int64) > lastChild {
		lastChild = int(maxExisting.Int64)
	}

	nextChild := lastChild + 1

	_, err = tx.ExecContext(ctx, `
		INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE last_child = ?
	`, parentID, nextChild, nextChild)
	if err != nil {
		return "", wrapExecError("get next child ID: update counter", err)
	}

	if err := tx.Commit(); err != nil {
		return "", wrapTransactionError("get next child ID: commit", err)
	}

	return fmt.Sprintf("%s.%d", parentID, nextChild), nil
}
