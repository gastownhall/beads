// Package sqlite implements dependency management for the SQLite storage backend.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const (
	// maxDependencyDepth is the maximum depth for recursive dependency traversal
	// to prevent infinite loops and limit query complexity
	maxDependencyDepth = 100
)

// AddDependency adds a dependency between issues with cycle prevention
func (s *SQLiteStorage) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	// Validate dependency type
	if !dep.Type.IsValid() {
		return fmt.Errorf("invalid dependency type: %q (must be non-empty string, max 50 chars)", dep.Type)
	}

	// Validate that source issue exists
	issueExists, err := s.GetIssue(ctx, dep.IssueID)
	if err != nil {
		return fmt.Errorf("failed to check issue %s: %w", dep.IssueID, err)
	}
	if issueExists == nil {
		return fmt.Errorf("issue %s not found", dep.IssueID)
	}

	// External refs (external:<project>:<capability>) don't need target validation
	// They are resolved lazily at query time by CheckExternalDep
	isExternalRef := strings.HasPrefix(dep.DependsOnID, "external:")

	var dependsOnExists *types.Issue
	if !isExternalRef {
		dependsOnExists, err = s.GetIssue(ctx, dep.DependsOnID)
		if err != nil {
			return fmt.Errorf("failed to check dependency %s: %w", dep.DependsOnID, err)
		}
		if dependsOnExists == nil {
			return fmt.Errorf("dependency target %s not found", dep.DependsOnID)
		}

		// Prevent self-dependency (only for local deps)
		if dep.IssueID == dep.DependsOnID {
			return fmt.Errorf("issue cannot depend on itself")
		}

		// Validate parent-child dependency direction (only for local deps)
		// In parent-child relationships: child depends on parent (child is part of parent)
		// Parent should NOT depend on child (semantically backwards)
		// Consistent with dependency semantics: IssueID depends on DependsOnID
		if dep.Type == types.DepParentChild {
			// issueExists is the dependent (the one that depends on something)
			// dependsOnExists is what it depends on
			// Correct: Task (child) depends on Epic (parent) - child belongs to parent
			// Incorrect: Epic (parent) depends on Task (child) - backwards
			if issueExists.IssueType == types.TypeEpic && dependsOnExists.IssueType != types.TypeEpic {
				return fmt.Errorf("invalid parent-child dependency: parent (%s) cannot depend on child (%s). Use: bd dep add %s %s --type parent-child",
					dep.IssueID, dep.DependsOnID, dep.DependsOnID, dep.IssueID)
			}
		}
	}

	if dep.CreatedAt.IsZero() {
		dep.CreatedAt = time.Now()
	}
	if dep.CreatedBy == "" {
		dep.CreatedBy = actor
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		// Cycle Detection and Prevention
		//
		// We prevent cycles across most dependency types to maintain a directed acyclic graph (DAG).
		// This is critical for:
		//
		// 1. Ready Work Calculation: Cycles can hide issues from the ready list by making them
		//    appear blocked when they're actually part of a circular dependency.
		//
		// 2. Dependency Traversal: Operations like dep tree and blocking propagation rely on
		//    DAG structure. Cycles would require special handling and could cause confusion.
		//
		// 3. Semantic Clarity: Circular dependencies are conceptually problematic - if A depends
		//    on B and B depends on A (directly or through other issues), which should be done first?
		//
		// EXCEPTION: relates-to links are inherently bidirectional ("see also" relationships).
		// When A relates-to B, we also create B relates-to A. This is not a cycle in the
		// problematic sense - it's a symmetric relationship that doesn't affect work ordering.
		//
		// Implementation: We use a recursive CTE to traverse from DependsOnID to see if we can
		// reach IssueID. If yes, adding "IssueID depends on DependsOnID" would complete a cycle.
		// We check ALL dependency types because cross-type cycles (e.g., A blocks B, B parent-child A)
		// are just as problematic as single-type cycles.
		//
		// The traversal is depth-limited to maxDependencyDepth (100) to prevent infinite loops
		// and excessive query cost. We check before inserting to avoid unnecessary write on failure.

		// Skip cycle detection for relates-to (inherently bidirectional)
		if dep.Type != types.DepRelatesTo {
			var cycleExists bool
			err = tx.QueryRowContext(ctx, `
				WITH RECURSIVE paths AS (
					SELECT
						issue_id,
						depends_on_id,
						1 as depth
					FROM dependencies
					WHERE issue_id = ?

					UNION ALL

					SELECT
						d.issue_id,
						d.depends_on_id,
						p.depth + 1
					FROM dependencies d
					JOIN paths p ON d.issue_id = p.depends_on_id
					WHERE p.depth < ?
				)
				SELECT EXISTS(
					SELECT 1 FROM paths
					WHERE depends_on_id = ?
				)
			`, dep.DependsOnID, maxDependencyDepth, dep.IssueID).Scan(&cycleExists)

			if err != nil {
				return fmt.Errorf("failed to check for cycles: %w", err)
			}

			if cycleExists {
				return fmt.Errorf("cannot add dependency: would create a cycle (%s → %s → ... → %s)",
					dep.IssueID, dep.DependsOnID, dep.IssueID)
			}
		}

	// Insert dependency (including metadata and thread_id for edge consolidation - Decision 004)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy, dep.Metadata, dep.ThreadID)
	if err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}

	// Record event
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, dep.IssueID, types.EventDependencyAdded, actor,
		fmt.Sprintf("Added dependency: %s %s %s", dep.IssueID, dep.Type, dep.DependsOnID))
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

		// Mark issues as dirty for incremental export
		// For external refs, only mark the source issue (target doesn't exist locally)
		issueIDsToMark := []string{dep.IssueID}
		if !isExternalRef {
			issueIDsToMark = append(issueIDsToMark, dep.DependsOnID)
		}
		if err := markIssuesDirtyTx(ctx, tx, issueIDsToMark); err != nil {
			return wrapDBError("mark issues dirty after adding dependency", err)
		}

		// Invalidate blocked issues cache since dependencies changed
		// Only invalidate for types that affect ready work calculation
		if dep.Type.AffectsReadyWork() {
			if err := s.invalidateBlockedCache(ctx, tx); err != nil {
				return fmt.Errorf("failed to invalidate blocked cache: %w", err)
			}
		}

		return nil
	})
}

// RemoveDependency removes a dependency
func (s *SQLiteStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		// First, check what type of dependency is being removed
		var depType types.DependencyType
		err := tx.QueryRowContext(ctx, `
			SELECT type FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
		`, issueID, dependsOnID).Scan(&depType)

		// Store whether cache needs invalidation before deletion
		needsCacheInvalidation := false
		if err == nil {
			needsCacheInvalidation = depType.AffectsReadyWork()
		}

		result, err := tx.ExecContext(ctx, `
			DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
		`, issueID, dependsOnID)
		if err != nil {
			return fmt.Errorf("failed to remove dependency: %w", err)
		}

		// Check if dependency existed
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to check rows affected: %w", err)
		}
		if rowsAffected == 0 {
			return fmt.Errorf("dependency from %s to %s does not exist", issueID, dependsOnID)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment)
			VALUES (?, ?, ?, ?)
		`, issueID, types.EventDependencyRemoved, actor,
			fmt.Sprintf("Removed dependency on %s", dependsOnID))
		if err != nil {
			return fmt.Errorf("failed to record event: %w", err)
		}

		// Mark issues as dirty for incremental export
		// For external refs, only mark the source issue (target doesn't exist locally)
		issueIDsToMark := []string{issueID}
		if !strings.HasPrefix(dependsOnID, "external:") {
			issueIDsToMark = append(issueIDsToMark, dependsOnID)
		}
		if err := markIssuesDirtyTx(ctx, tx, issueIDsToMark); err != nil {
			return wrapDBError("mark issues dirty after removing dependency", err)
		}

		// Invalidate blocked issues cache if this was a blocking dependency
		if needsCacheInvalidation {
			if err := s.invalidateBlockedCache(ctx, tx); err != nil {
				return fmt.Errorf("failed to invalidate blocked cache: %w", err)
			}
		}

		return nil
	})
}

// GetDependenciesWithMetadata returns issues that this issue depends on, including dependency type
func (s *SQLiteStorage) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.created_by, i.updated_at, i.closed_at, i.external_ref, i.source_repo,
		       i.deleted_at, i.deleted_by, i.delete_reason, i.original_type,
		       i.sender, i.ephemeral, i.pinned, i.is_template,
		       i.await_type, i.await_id, i.timeout_ns, i.waiters,
		       d.type
		FROM issues i
		JOIN dependencies d ON i.id = d.depends_on_id
		WHERE d.issue_id = ?
		ORDER BY i.priority ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies with metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssuesWithDependencyType(ctx, rows)
}

// GetDependentsWithMetadata returns issues that depend on this issue, including dependency type
func (s *SQLiteStorage) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.created_by, i.updated_at, i.closed_at, i.external_ref, i.source_repo,
		       i.deleted_at, i.deleted_by, i.delete_reason, i.original_type,
		       i.sender, i.ephemeral, i.pinned, i.is_template,
		       i.await_type, i.await_id, i.timeout_ns, i.waiters,
		       d.type
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		WHERE d.depends_on_id = ?
		ORDER BY i.priority ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependents with metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssuesWithDependencyType(ctx, rows)
}

// GetDependencies returns issues that this issue depends on
func (s *SQLiteStorage) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	issuesWithMeta, err := s.GetDependenciesWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}

	// Convert to plain Issue slice for backward compatibility
	issues := make([]*types.Issue, len(issuesWithMeta))
	for i, iwm := range issuesWithMeta {
		issues[i] = &iwm.Issue
	}
	return issues, nil
}

// GetDependents returns issues that depend on this issue
func (s *SQLiteStorage) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	issuesWithMeta, err := s.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}

	// Convert to plain Issue slice for backward compatibility
	issues := make([]*types.Issue, len(issuesWithMeta))
	for i, iwm := range issuesWithMeta {
		issues[i] = &iwm.Issue
	}
	return issues, nil
}

// GetDependencyCounts returns dependency and dependent counts for multiple issues in a single query
func (s *SQLiteStorage) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	if len(issueIDs) == 0 {
		return make(map[string]*types.DependencyCounts), nil
	}

	// Build placeholders for the IN clause
	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs)*2)
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
		args[len(issueIDs)+i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Single query that counts both dependencies and dependents
	// Uses UNION ALL to combine results from both directions
	query := fmt.Sprintf(`
		SELECT
			issue_id,
			SUM(CASE WHEN type = 'dependency' THEN count ELSE 0 END) as dependency_count,
			SUM(CASE WHEN type = 'dependent' THEN count ELSE 0 END) as dependent_count
		FROM (
			-- Count dependencies (issues this issue depends on)
			SELECT issue_id, 'dependency' as type, COUNT(*) as count
			FROM dependencies
			WHERE issue_id IN (%s)
			GROUP BY issue_id

			UNION ALL

			-- Count dependents (issues that depend on this issue)
			SELECT depends_on_id as issue_id, 'dependent' as type, COUNT(*) as count
			FROM dependencies
			WHERE depends_on_id IN (%s)
			GROUP BY depends_on_id
		)
		GROUP BY issue_id
	`, inClause, inClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency counts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]*types.DependencyCounts)
	for rows.Next() {
		var issueID string
		var counts types.DependencyCounts
		if err := rows.Scan(&issueID, &counts.DependencyCount, &counts.DependentCount); err != nil {
			return nil, fmt.Errorf("failed to scan dependency counts: %w", err)
		}
		result[issueID] = &counts
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating dependency counts: %w", err)
	}

	// Fill in zero counts for issues with no dependencies or dependents
	for _, id := range issueIDs {
		if _, exists := result[id]; !exists {
			result[id] = &types.DependencyCounts{
				DependencyCount: 0,
				DependentCount:  0,
			}
		}
	}

	return result, nil
}

// GetDependencyRecords returns raw dependency records for an issue
func (s *SQLiteStorage) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by,
		       COALESCE(metadata, '{}') as metadata, COALESCE(thread_id, '') as thread_id
		FROM dependencies
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var deps []*types.Dependency
	for rows.Next() {
		var dep types.Dependency
		err := rows.Scan(
			&dep.IssueID,
			&dep.DependsOnID,
			&dep.Type,
			&dep.CreatedAt,
			&dep.CreatedBy,
			&dep.Metadata,
			&dep.ThreadID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan dependency: %w", err)
		}
		deps = append(deps, &dep)
	}

	return deps, nil
}

// GetAllDependencyRecords returns all dependency records grouped by issue ID
// This is optimized for bulk export operations to avoid N+1 queries
func (s *SQLiteStorage) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by,
		       COALESCE(metadata, '{}') as metadata, COALESCE(thread_id, '') as thread_id
		FROM dependencies
		ORDER BY issue_id, created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get all dependency records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Group dependencies by issue ID
	depsMap := make(map[string][]*types.Dependency)
	for rows.Next() {
		var dep types.Dependency
		err := rows.Scan(
			&dep.IssueID,
			&dep.DependsOnID,
			&dep.Type,
			&dep.CreatedAt,
			&dep.CreatedBy,
			&dep.Metadata,
			&dep.ThreadID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan dependency: %w", err)
		}
		depsMap[dep.IssueID] = append(depsMap[dep.IssueID], &dep)
	}

	return depsMap, nil
}

// GetDependencyTree returns the full dependency tree with optional deduplication
// When showAllPaths is false (default), nodes appearing via multiple paths (diamond dependencies)
// appear only once at their shallowest depth in the tree.
// When showAllPaths is true, all paths are shown with duplicate nodes at different depths.
// When reverse is true, shows dependent tree (what was discovered from this) instead of dependency tree (what blocks this).
func (s *SQLiteStorage) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	if maxDepth <= 0 {
		maxDepth = 50
	}

	// Build SQL query based on direction
	// Normal mode: traverse dependencies (what blocks me) - goes UP
	// Reverse mode: traverse dependents (what was discovered from me) - goes DOWN
	var query string
	if reverse {
		// Reverse: show dependents (what depends on this issue)
		query = `
			WITH RECURSIVE tree AS (
				SELECT
				i.id, i.title, i.status, i.priority, i.description, i.design,
				i.acceptance_criteria, i.notes, i.issue_type, i.assignee,
				i.estimated_minutes, i.created_at, i.created_by, i.updated_at, i.closed_at,
				i.external_ref,
				0 as depth,
				i.id as path,
				i.id as parent_id
				FROM issues i
				WHERE i.id = ?

				UNION ALL

				SELECT
				i.id, i.title, i.status, i.priority, i.description, i.design,
				i.acceptance_criteria, i.notes, i.issue_type, i.assignee,
				i.estimated_minutes, i.created_at, i.created_by, i.updated_at, i.closed_at,
				i.external_ref,
				t.depth + 1,
				t.path || '→' || i.id,
				t.id
				FROM issues i
				JOIN dependencies d ON i.id = d.issue_id
				JOIN tree t ON d.depends_on_id = t.id
				WHERE t.depth < ?
				AND t.path != i.id
			AND t.path NOT LIKE i.id || '→%'
			AND t.path NOT LIKE '%→' || i.id || '→%'
			AND t.path NOT LIKE '%→' || i.id
				)
				SELECT id, title, status, priority, description, design,
				acceptance_criteria, notes, issue_type, assignee,
				estimated_minutes, created_at, updated_at, closed_at,
				external_ref, depth, parent_id
				FROM tree
				ORDER BY depth, priority, id
		`
	} else {
		// Normal: show dependencies (what this issue depends on)
		query = `
			WITH RECURSIVE tree AS (
				SELECT
				i.id, i.title, i.status, i.priority, i.description, i.design,
				i.acceptance_criteria, i.notes, i.issue_type, i.assignee,
				i.estimated_minutes, i.created_at, i.created_by, i.updated_at, i.closed_at,
				i.external_ref,
				0 as depth,
				i.id as path,
				i.id as parent_id
				FROM issues i
				WHERE i.id = ?

				UNION ALL

				SELECT
				i.id, i.title, i.status, i.priority, i.description, i.design,
				i.acceptance_criteria, i.notes, i.issue_type, i.assignee,
				i.estimated_minutes, i.created_at, i.created_by, i.updated_at, i.closed_at,
				i.external_ref,
				t.depth + 1,
				t.path || '→' || i.id,
				t.id
				FROM issues i
				JOIN dependencies d ON i.id = d.depends_on_id
				JOIN tree t ON d.issue_id = t.id
				WHERE t.depth < ?
				AND t.path != i.id
			AND t.path NOT LIKE i.id || '→%'
			AND t.path NOT LIKE '%→' || i.id || '→%'
			AND t.path NOT LIKE '%→' || i.id
				)
				SELECT id, title, status, priority, description, design,
				acceptance_criteria, notes, issue_type, assignee,
				estimated_minutes, created_at, updated_at, closed_at,
				external_ref, depth, parent_id
				FROM tree
				ORDER BY depth, priority, id
		`
	}

	// First, build the complete tree with all paths using recursive CTE
	// We need to track the full path to handle proper tree structure
	rows, err := s.db.QueryContext(ctx, query, issueID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency tree: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Use a map to track nodes we've seen and deduplicate
	// Key: issue ID, Value: minimum depth where we saw it
	seen := make(map[string]int)
	var nodes []*types.TreeNode

	for rows.Next() {
		var node types.TreeNode
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString
		var parentID string // Currently unused, but available for future parent relationship display

		err := rows.Scan(
			&node.ID, &node.Title, &node.Status, &node.Priority,
			&node.Description, &node.Design, &node.AcceptanceCriteria,
			&node.Notes, &node.IssueType, &assignee, &estimatedMinutes,
			&node.CreatedAt, &node.UpdatedAt, &closedAt, &externalRef,
			&node.Depth, &parentID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan tree node: %w", err)
		}
		node.ParentID = parentID

		if closedAt.Valid {
			node.ClosedAt = &closedAt.Time
		}
		if estimatedMinutes.Valid {
			mins := int(estimatedMinutes.Int64)
			node.EstimatedMinutes = &mins
		}
		if assignee.Valid {
			node.Assignee = assignee.String
		}
		if externalRef.Valid {
			node.ExternalRef = &externalRef.String
		}

		node.Truncated = node.Depth == maxDepth

		// Deduplicate only if showAllPaths is false
		if !showAllPaths {
			// Only include a node the first time we see it (shallowest depth)
			// Since we ORDER BY depth, priority, id - the first occurrence is at minimum depth
			if prevDepth, exists := seen[node.ID]; exists {
				// We've seen this node before at depth prevDepth
				// Skip this duplicate occurrence
				_ = prevDepth // Avoid unused variable warning
				continue
			}

			// Mark this node as seen at this depth
			seen[node.ID] = node.Depth
		}
		nodes = append(nodes, &node)
	}

	// Fetch external dependencies for all issues in the tree
	// External deps like "external:project:capability" don't exist in the issues
	// table, so the recursive CTE above doesn't find them. We add them as
	// synthetic leaf nodes here.
	if len(nodes) > 0 && !reverse {
		// Collect all issue IDs in the tree
		issueIDs := make([]string, len(nodes))
		depthByID := make(map[string]int)
		for i, n := range nodes {
			issueIDs[i] = n.ID
			depthByID[n.ID] = n.Depth
		}

		// Query for external dependencies
		externalDeps, err := s.getExternalDepsForIssues(ctx, issueIDs)
		if err != nil {
			// Non-fatal: just skip external deps if query fails
			_ = err
		} else {
			// Create synthetic TreeNode for each external dep
			for parentID, extRefs := range externalDeps {
				parentDepth, ok := depthByID[parentID]
				if !ok {
					continue
				}
				// Skip if we've exceeded maxDepth
				if parentDepth >= maxDepth {
					continue
				}

				for _, ref := range extRefs {
					// Parse external ref for display
					_, capability := parseExternalRefParts(ref)
					if capability == "" {
						capability = ref // fallback to full ref
					}

					// Check resolution status
					status := CheckExternalDep(ctx, ref)
					var nodeStatus types.Status
					var title string
					if status.Satisfied {
						nodeStatus = types.StatusClosed
						title = fmt.Sprintf("✓ %s", capability)
					} else {
						nodeStatus = types.StatusBlocked
						title = fmt.Sprintf("⏳ %s", capability)
					}

					extNode := &types.TreeNode{
						Issue: types.Issue{
							ID:        ref,
							Title:     title,
							Status:    nodeStatus,
							Priority:  0, // External deps don't have priority
							IssueType: types.TypeTask,
						},
						Depth:    parentDepth + 1,
						ParentID: parentID,
					}

					// Apply deduplication if needed
					if !showAllPaths {
						if _, exists := seen[ref]; exists {
							continue
						}
						seen[ref] = extNode.Depth
					}

					nodes = append(nodes, extNode)
				}
			}
		}
	}

	return nodes, nil
}

// parseExternalRefParts parses "external:project:capability" and returns (project, capability).
// Returns empty strings if the format is invalid.
func parseExternalRefParts(ref string) (project, capability string) {
	if !strings.HasPrefix(ref, "external:") {
		return "", ""
	}
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 {
		return "", ""
	}
	return parts[1], parts[2]
}

// loadDependencyGraph loads all non-relates-to dependencies as an adjacency list.
// This is used by DetectCycles for O(V+E) cycle detection instead of the O(2^n) SQL CTE.
func (s *SQLiteStorage) loadDependencyGraph(ctx context.Context) (map[string][]string, error) {
	deps := make(map[string][]string)
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id, depends_on_id
		FROM dependencies
		WHERE type != 'relates-to'
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to load dependency graph: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		deps[from] = append(deps[from], to)
	}
	return deps, rows.Err()
}

// DetectCycles finds circular dependencies and returns the actual cycle paths.
// Uses O(V+E) DFS with shared visited set instead of O(2^n) SQL path enumeration.
// Note: relates-to dependencies are excluded because they are intentionally bidirectional
// ("see also" relationships) and do not represent problematic cycles.
func (s *SQLiteStorage) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	// Load all dependencies as adjacency list (one query)
	deps, err := s.loadDependencyGraph(ctx)
	if err != nil {
		return nil, err
	}

	// DFS with shared visited set - O(V+E)
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	var allCycles [][]string

	// Recursive DFS function
	var dfs func(node string, path []string)
	dfs = func(node string, path []string) {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)

		for _, neighbor := range deps[node] {
			if !visited[neighbor] {
				dfs(neighbor, path)
			} else if recStack[neighbor] {
				// Found cycle - extract the cycle portion
				cycleStart := -1
				for i, n := range path {
					if n == neighbor {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					cycle := make([]string, len(path)-cycleStart)
					copy(cycle, path[cycleStart:])
					allCycles = append(allCycles, cycle)
				}
			}
		}

		recStack[node] = false
	}

	// Check from each unvisited node
	for node := range deps {
		if !visited[node] {
			dfs(node, nil)
		}
	}

	// Deduplicate cycles (same cycle can be found from different entry points)
	seen := make(map[string]bool)
	var uniqueCycles [][]string
	for _, cycle := range allCycles {
		// Normalize cycle: rotate to start with smallest ID
		normalized := normalizeCycle(cycle)
		key := strings.Join(normalized, "→")
		if !seen[key] {
			seen[key] = true
			uniqueCycles = append(uniqueCycles, normalized)
		}
	}

	// Convert cycle paths to Issue objects
	var cycles [][]*types.Issue
	for _, cyclePath := range uniqueCycles {
		var cycleIssues []*types.Issue
		for _, issueID := range cyclePath {
			issue, err := s.GetIssue(ctx, issueID)
			if err != nil {
				return nil, fmt.Errorf("failed to get issue %s: %w", issueID, err)
			}
			if issue != nil {
				cycleIssues = append(cycleIssues, issue)
			}
		}
		if len(cycleIssues) > 0 {
			cycles = append(cycles, cycleIssues)
		}
	}

	return cycles, nil
}

// normalizeCycle rotates a cycle to start with the lexicographically smallest ID.
// This ensures the same cycle found from different entry points is deduplicated.
func normalizeCycle(cycle []string) []string {
	if len(cycle) == 0 {
		return cycle
	}

	// Find index of smallest element
	minIdx := 0
	for i, id := range cycle {
		if id < cycle[minIdx] {
			minIdx = i
		}
	}

	// Rotate to start with smallest
	result := make([]string, len(cycle))
	for i := 0; i < len(cycle); i++ {
		result[i] = cycle[(minIdx+i)%len(cycle)]
	}
	return result
}

// Helper function to scan issues from rows
func (s *SQLiteStorage) scanIssues(ctx context.Context, rows *sql.Rows) ([]*types.Issue, error) {
	var issues []*types.Issue
	var issueIDs []string

	// First pass: scan all issues
	for rows.Next() {
		var issue types.Issue
		var contentHash sql.NullString
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString
		var sourceRepo sql.NullString
		var closeReason sql.NullString
		var deletedAt sql.NullString // TEXT column, not DATETIME - must parse manually
		var deletedBy sql.NullString
		var deleteReason sql.NullString
		var originalType sql.NullString
		// Messaging fields
		var sender sql.NullString
		var wisp sql.NullInt64
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
			&issue.CreatedAt, &issue.CreatedBy, &issue.UpdatedAt, &closedAt, &externalRef, &sourceRepo, &closeReason,
			&deletedAt, &deletedBy, &deleteReason, &originalType,
			&sender, &wisp, &pinned, &isTemplate,
			&awaitType, &awaitID, &timeoutNs, &waiters,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan issue: %w", err)
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
		if wisp.Valid && wisp.Int64 != 0 {
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
		issueIDs = append(issueIDs, issue.ID)
	}

	// Second pass: batch-load labels for all issues
	labelsMap, err := s.GetLabelsForIssues(ctx, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get labels: %w", err)
	}

	// Assign labels to issues
	for _, issue := range issues {
		if labels, ok := labelsMap[issue.ID]; ok {
			issue.Labels = labels
		}
	}

	return issues, nil
}

// Helper function to scan issues with dependency type from rows
func (s *SQLiteStorage) scanIssuesWithDependencyType(ctx context.Context, rows *sql.Rows) ([]*types.IssueWithDependencyMetadata, error) {
	var results []*types.IssueWithDependencyMetadata
	for rows.Next() {
		var issue types.Issue
		var contentHash sql.NullString
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString
		var sourceRepo sql.NullString
		var deletedAt sql.NullString // TEXT column, not DATETIME - must parse manually
		var deletedBy sql.NullString
		var deleteReason sql.NullString
		var originalType sql.NullString
		// Messaging fields
		var sender sql.NullString
		var wisp sql.NullInt64
		// Pinned field
		var pinned sql.NullInt64
		// Template field
		var isTemplate sql.NullInt64
		// Gate fields
		var awaitType sql.NullString
		var awaitID sql.NullString
		var timeoutNs sql.NullInt64
		var waiters sql.NullString
		var depType types.DependencyType

		err := rows.Scan(
			&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&issue.CreatedAt, &issue.CreatedBy, &issue.UpdatedAt, &closedAt, &externalRef, &sourceRepo,
			&deletedAt, &deletedBy, &deleteReason, &originalType,
			&sender, &wisp, &pinned, &isTemplate,
			&awaitType, &awaitID, &timeoutNs, &waiters,
			&depType,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan issue with dependency type: %w", err)
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
		if wisp.Valid && wisp.Int64 != 0 {
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

		// Fetch labels for this issue
		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get labels for issue %s: %w", issue.ID, err)
		}
		issue.Labels = labels

		result := &types.IssueWithDependencyMetadata{
			Issue:          issue,
			DependencyType: depType,
		}
		results = append(results, result)
	}

	return results, nil
}
