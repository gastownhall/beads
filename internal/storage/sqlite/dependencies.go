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
		return fmt.Errorf("invalid dependency type: %s (must be blocks, related, parent-child, or discovered-from)", dep.Type)
	}

	// Validate that both issues exist
	issueExists, err := s.GetIssue(ctx, dep.IssueID)
	if err != nil {
		return fmt.Errorf("failed to check issue %s: %w", dep.IssueID, err)
	}
	if issueExists == nil {
		return fmt.Errorf("issue %s not found", dep.IssueID)
	}

	dependsOnExists, err := s.GetIssue(ctx, dep.DependsOnID)
	if err != nil {
		return fmt.Errorf("failed to check dependency %s: %w", dep.DependsOnID, err)
	}
	if dependsOnExists == nil {
		return fmt.Errorf("dependency target %s not found", dep.DependsOnID)
	}

	// Prevent self-dependency
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("issue cannot depend on itself")
	}

	// Validate parent-child dependency direction
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

	if dep.CreatedAt.IsZero() {
		dep.CreatedAt = time.Now()
	}
	if dep.CreatedBy == "" {
		dep.CreatedBy = actor
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Cycle Detection and Prevention
	//
	// We prevent cycles across ALL dependency types (blocks, related, parent-child, discovered-from)
	// to maintain a directed acyclic graph (DAG). This is critical for:
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
	// Implementation: We use a recursive CTE to traverse from DependsOnID to see if we can
	// reach IssueID. If yes, adding "IssueID depends on DependsOnID" would complete a cycle.
	// We check ALL dependency types because cross-type cycles (e.g., A blocks B, B parent-child A)
	// are just as problematic as single-type cycles.
	//
	// The traversal is depth-limited to maxDependencyDepth (100) to prevent infinite loops
	// and excessive query cost. We check before inserting to avoid unnecessary write on failure.
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

	// Insert dependency
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
		VALUES (?, ?, ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy)
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

	// Mark both issues as dirty for incremental export
	// (dependencies are exported with each issue, so both need updating)
	if err := markIssuesDirtyTx(ctx, tx, []string{dep.IssueID, dep.DependsOnID}); err != nil {
		return err
	}

	return tx.Commit()
}

// addDependencyUnchecked adds a dependency with minimal validation, used during
// import/remap operations where we're preserving existing dependencies with new IDs.
// Skips semantic validation (parent-child direction) but keeps essential checks:
// - Issue existence validation
// - Self-dependency prevention
// - Cycle detection
func (s *SQLiteStorage) addDependencyUnchecked(ctx context.Context, dep *types.Dependency, actor string) error {
	// Validate dependency type
	if !dep.Type.IsValid() {
		return fmt.Errorf("invalid dependency type: %s", dep.Type)
	}

	// Validate that both issues exist
	issueExists, err := s.GetIssue(ctx, dep.IssueID)
	if err != nil {
		return fmt.Errorf("failed to check issue %s: %w", dep.IssueID, err)
	}
	if issueExists == nil {
		return fmt.Errorf("issue %s not found", dep.IssueID)
	}

	dependsOnExists, err := s.GetIssue(ctx, dep.DependsOnID)
	if err != nil {
		return fmt.Errorf("failed to check dependency %s: %w", dep.DependsOnID, err)
	}
	if dependsOnExists == nil {
		return fmt.Errorf("dependency target %s not found", dep.DependsOnID)
	}

	// Prevent self-dependency
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("issue cannot depend on itself")
	}

	// NOTE: We skip parent-child direction validation here because during import/remap,
	// we're just updating IDs on existing dependencies that were already validated.

	if dep.CreatedAt.IsZero() {
		dep.CreatedAt = time.Now()
	}
	if dep.CreatedBy == "" {
		dep.CreatedBy = actor
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Cycle detection (same as AddDependency)
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

	// Insert dependency
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
		VALUES (?, ?, ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy)
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

	// Mark both issues as dirty
	if err := markIssuesDirtyTx(ctx, tx, []string{dep.IssueID, dep.DependsOnID}); err != nil {
		return err
	}

	return tx.Commit()
}

// RemoveDependency removes a dependency
func (s *SQLiteStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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

	// Mark both issues as dirty for incremental export
	if err := markIssuesDirtyTx(ctx, tx, []string{issueID, dependsOnID}); err != nil {
		return err
	}

	return tx.Commit()
}

// removeDependencyIfExists removes a dependency, returning nil if it doesn't exist
// This is useful during remapping where dependencies may have been already removed
func (s *SQLiteStorage) removeDependencyIfExists(ctx context.Context, issueID, dependsOnID string, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID)
	if err != nil {
		return fmt.Errorf("failed to remove dependency: %w", err)
	}

	// Check if dependency existed - if not, that's okay, just skip the event
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rowsAffected == 0 {
		// Dependency didn't exist, nothing to do
		return nil
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventDependencyRemoved, actor,
		fmt.Sprintf("Removed dependency on %s", dependsOnID))
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark both issues as dirty for incremental export
	if err := markIssuesDirtyTx(ctx, tx, []string{issueID, dependsOnID}); err != nil {
		return err
	}

	return tx.Commit()
}

// GetDependencies returns issues that this issue depends on
func (s *SQLiteStorage) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.updated_at, i.closed_at, i.external_ref
		FROM issues i
		JOIN dependencies d ON i.id = d.depends_on_id
		WHERE d.issue_id = ?
		ORDER BY i.priority ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
}

// GetDependents returns issues that depend on this issue
func (s *SQLiteStorage) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.updated_at, i.closed_at, i.external_ref
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		WHERE d.depends_on_id = ?
		ORDER BY i.priority ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
}

// GetDependencyRecords returns raw dependency records for an issue
func (s *SQLiteStorage) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by
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
		SELECT issue_id, depends_on_id, type, created_at, created_by
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
				i.estimated_minutes, i.created_at, i.updated_at, i.closed_at,
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
				i.estimated_minutes, i.created_at, i.updated_at, i.closed_at,
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
				i.estimated_minutes, i.created_at, i.updated_at, i.closed_at,
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
				i.estimated_minutes, i.created_at, i.updated_at, i.closed_at,
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
		_ = parentID // Silence unused variable warning

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

	return nodes, nil
}

// DetectCycles finds circular dependencies and returns the actual cycle paths
func (s *SQLiteStorage) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	// Use recursive CTE to find cycles with full paths
	// We track the path as a string to work around SQLite's lack of arrays
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE paths AS (
			SELECT
				issue_id,
				depends_on_id,
				issue_id as start_id,
				issue_id || '→' || depends_on_id as path,
				0 as depth
			FROM dependencies

			UNION ALL

			SELECT
				d.issue_id,
				d.depends_on_id,
				p.start_id,
				p.path || '→' || d.depends_on_id,
				p.depth + 1
			FROM dependencies d
			JOIN paths p ON d.issue_id = p.depends_on_id
			WHERE p.depth < ?
			  AND p.path NOT LIKE '%' || d.depends_on_id || '→%'
		)
		SELECT DISTINCT path || '→' || start_id as cycle_path
		FROM paths
		WHERE depends_on_id = start_id
		ORDER BY cycle_path
	`, maxDependencyDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to detect cycles: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var cycles [][]*types.Issue
	seen := make(map[string]bool)

	for rows.Next() {
		var pathStr string
		if err := rows.Scan(&pathStr); err != nil {
			return nil, err
		}

		// Skip if we've already seen this cycle (can happen with different entry points)
		if seen[pathStr] {
			continue
		}
		seen[pathStr] = true

		// Parse the path string: "bd-1→bd-2→bd-3→bd-1"
		issueIDs := strings.Split(pathStr, "→")

		// Remove the duplicate last element (cycle closes back to start)
		if len(issueIDs) > 1 && issueIDs[0] == issueIDs[len(issueIDs)-1] {
			issueIDs = issueIDs[:len(issueIDs)-1]
		}

		// Fetch full issue details for each ID in the cycle
		var cycleIssues []*types.Issue
		for _, issueID := range issueIDs {
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

// Helper function to scan issues from rows
func (s *SQLiteStorage) scanIssues(ctx context.Context, rows *sql.Rows) ([]*types.Issue, error) {
	var issues []*types.Issue
	for rows.Next() {
		var issue types.Issue
		var contentHash sql.NullString
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString

		err := rows.Scan(
			&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
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

		// Fetch labels for this issue
		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get labels for issue %s: %w", issue.ID, err)
		}
		issue.Labels = labels

		issues = append(issues, &issue)
	}

	return issues, nil
}
