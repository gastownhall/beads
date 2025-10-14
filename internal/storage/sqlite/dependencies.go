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

	dep.CreatedAt = time.Now()
	dep.CreatedBy = actor

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert dependency
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
		VALUES (?, ?, ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy)
	if err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}

	// Check if this creates a cycle (only for 'blocks' type dependencies)
	// We need to check if we can reach IssueID from DependsOnID
	// If yes, adding "IssueID depends on DependsOnID" would create a cycle
	if dep.Type == types.DepBlocks {
		var cycleExists bool
		err = tx.QueryRowContext(ctx, `
			WITH RECURSIVE paths AS (
				SELECT
					issue_id,
					depends_on_id,
					1 as depth
				FROM dependencies
				WHERE type = 'blocks'
				  AND issue_id = ?

				UNION ALL

				SELECT
					d.issue_id,
					d.depends_on_id,
					p.depth + 1
				FROM dependencies d
				JOIN paths p ON d.issue_id = p.depends_on_id
				WHERE d.type = 'blocks'
				  AND p.depth < 100
			)
			SELECT EXISTS(
				SELECT 1 FROM paths
				WHERE depends_on_id = ?
			)
		`, dep.DependsOnID, dep.IssueID).Scan(&cycleExists)

		if err != nil {
			return fmt.Errorf("failed to check for cycles: %w", err)
		}

		if cycleExists {
			return fmt.Errorf("cannot add dependency: would create a cycle (%s → %s → ... → %s)",
				dep.IssueID, dep.DependsOnID, dep.IssueID)
		}
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

// RemoveDependency removes a dependency
func (s *SQLiteStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID)
	if err != nil {
		return fmt.Errorf("failed to remove dependency: %w", err)
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
		SELECT i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
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
	defer rows.Close()

	return scanIssues(rows)
}

// GetDependents returns issues that depend on this issue
func (s *SQLiteStorage) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
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
	defer rows.Close()

	return scanIssues(rows)
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
	defer rows.Close()

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
	defer rows.Close()

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

// GetDependencyTree returns the full dependency tree
func (s *SQLiteStorage) GetDependencyTree(ctx context.Context, issueID string, maxDepth int) ([]*types.TreeNode, error) {
	if maxDepth <= 0 {
		maxDepth = 50
	}

	// Use recursive CTE to build tree
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE tree AS (
			SELECT
				i.id, i.title, i.status, i.priority, i.description, i.design,
				i.acceptance_criteria, i.notes, i.issue_type, i.assignee,
				i.estimated_minutes, i.created_at, i.updated_at, i.closed_at,
				i.external_ref,
				0 as depth
			FROM issues i
			WHERE i.id = ?

			UNION ALL

			SELECT
				i.id, i.title, i.status, i.priority, i.description, i.design,
				i.acceptance_criteria, i.notes, i.issue_type, i.assignee,
				i.estimated_minutes, i.created_at, i.updated_at, i.closed_at,
				i.external_ref,
				t.depth + 1
			FROM issues i
			JOIN dependencies d ON i.id = d.depends_on_id
			JOIN tree t ON d.issue_id = t.id
			WHERE t.depth < ?
		)
		SELECT * FROM tree
		ORDER BY depth, priority
	`, issueID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency tree: %w", err)
	}
	defer rows.Close()

	var nodes []*types.TreeNode
	for rows.Next() {
		var node types.TreeNode
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString

		err := rows.Scan(
			&node.ID, &node.Title, &node.Status, &node.Priority,
			&node.Description, &node.Design, &node.AcceptanceCriteria,
			&node.Notes, &node.IssueType, &assignee, &estimatedMinutes,
			&node.CreatedAt, &node.UpdatedAt, &closedAt, &externalRef, &node.Depth,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan tree node: %w", err)
		}

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
			WHERE p.depth < 100
			  AND p.path NOT LIKE '%' || d.depends_on_id || '→%'
		)
		SELECT DISTINCT path || '→' || start_id as cycle_path
		FROM paths
		WHERE depends_on_id = start_id
		ORDER BY cycle_path
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to detect cycles: %w", err)
	}
	defer rows.Close()

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
func scanIssues(rows *sql.Rows) ([]*types.Issue, error) {
	var issues []*types.Issue
	for rows.Next() {
		var issue types.Issue
		var closedAt sql.NullTime
		var estimatedMinutes sql.NullInt64
		var assignee sql.NullString
		var externalRef sql.NullString

		err := rows.Scan(
			&issue.ID, &issue.Title, &issue.Description, &issue.Design,
			&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
			&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
			&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan issue: %w", err)
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

		issues = append(issues, &issue)
	}

	return issues, nil
}
