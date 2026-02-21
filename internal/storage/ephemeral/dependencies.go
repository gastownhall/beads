package ephemeral

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// AddDependency adds a dependency between two ephemeral issues.
func (s *Store) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO dependencies (issue_id, depends_on_id, type, created_by, metadata, thread_id)
		 VALUES (?, ?, ?, ?, '{}', ?)`,
		dep.IssueID, dep.DependsOnID, dep.Type, actor, dep.ThreadID)
	if err != nil {
		return fmt.Errorf("add ephemeral dependency: %w", err)
	}
	return nil
}

// RemoveDependency removes a dependency.
func (s *Store) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?`,
		issueID, dependsOnID)
	if err != nil {
		return fmt.Errorf("remove ephemeral dependency: %w", err)
	}
	return nil
}

// GetDependencies returns issues that this issue depends on.
func (s *Store) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+issueSelectColumns+` FROM issues
		 WHERE id IN (SELECT depends_on_id FROM dependencies WHERE issue_id = ?)`, issueID)
	if err != nil {
		return nil, fmt.Errorf("get ephemeral dependencies: %w", err)
	}
	defer rows.Close()
	return scanIssues(rows)
}

// GetDependents returns issues that depend on this issue.
func (s *Store) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+issueSelectColumns+` FROM issues
		 WHERE id IN (SELECT issue_id FROM dependencies WHERE depends_on_id = ?)`, issueID)
	if err != nil {
		return nil, fmt.Errorf("get ephemeral dependents: %w", err)
	}
	defer rows.Close()
	return scanIssues(rows)
}

// GetDependencyRecords returns raw dependency records for an issue.
func (s *Store) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT issue_id, depends_on_id, type, created_at, created_by, thread_id
		 FROM dependencies WHERE issue_id = ?`, issueID)
	if err != nil {
		return nil, fmt.Errorf("get ephemeral dependency records: %w", err)
	}
	defer rows.Close()

	var deps []*types.Dependency
	for rows.Next() {
		var d types.Dependency
		var createdAt, threadID sql.NullString
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &createdAt, &d.CreatedBy, &threadID); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			d.CreatedAt = parseTime(createdAt.String)
		}
		if threadID.Valid {
			d.ThreadID = threadID.String
		}
		deps = append(deps, &d)
	}
	return deps, rows.Err()
}

// GetDependencyRecordsForIssues returns dependency records for multiple issues.
func (s *Store) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(issueIDs))
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT issue_id, depends_on_id, type, created_at, created_by, thread_id
		 FROM dependencies WHERE issue_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("get ephemeral dependency records for issues: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*types.Dependency)
	for rows.Next() {
		var d types.Dependency
		var createdAt, threadID sql.NullString
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &createdAt, &d.CreatedBy, &threadID); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			d.CreatedAt = parseTime(createdAt.String)
		}
		if threadID.Valid {
			d.ThreadID = threadID.String
		}
		result[d.IssueID] = append(result[d.IssueID], &d)
	}
	return result, rows.Err()
}

// GetBlockingInfoForIssues returns blocking info for a set of issues.
func (s *Store) GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (blockedByMap map[string][]string, blocksMap map[string][]string, err error) {
	blockedByMap = make(map[string][]string)
	blocksMap = make(map[string][]string)

	if len(issueIDs) == 0 {
		return
	}

	placeholders := make([]string, len(issueIDs))
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	in := strings.Join(placeholders, ",")

	// Issues that block us (where we are the issue_id)
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.issue_id, d.depends_on_id FROM dependencies d
		 JOIN issues i ON i.id = d.depends_on_id
		 WHERE d.issue_id IN (`+in+`)
		   AND d.type = 'blocks'
		   AND i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')`,
		args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, depID string
		if err := rows.Scan(&issueID, &depID); err != nil {
			return nil, nil, err
		}
		blockedByMap[issueID] = append(blockedByMap[issueID], depID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Issues we block (where we are the depends_on_id)
	rows2, err := s.db.QueryContext(ctx,
		`SELECT d.depends_on_id, d.issue_id FROM dependencies d
		 WHERE d.depends_on_id IN (`+in+`)
		   AND d.type = 'blocks'`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var depID, issueID string
		if err := rows2.Scan(&depID, &issueID); err != nil {
			return nil, nil, err
		}
		blocksMap[depID] = append(blocksMap[depID], issueID)
	}
	return blockedByMap, blocksMap, rows2.Err()
}

// GetDependenciesWithMetadata returns dependencies with their dep type.
func (s *Store) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.depends_on_id, d.type FROM dependencies d WHERE d.issue_id = ?`, issueID)
	if err != nil {
		return nil, fmt.Errorf("get ephemeral deps with metadata: %w", err)
	}
	defer rows.Close()

	type depMeta struct{ depID, depType string }
	var deps []depMeta
	for rows.Next() {
		var dm depMeta
		if err := rows.Scan(&dm.depID, &dm.depType); err != nil {
			return nil, err
		}
		deps = append(deps, dm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return nil, nil
	}

	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// GetDependentsWithMetadata returns dependents with their dep type.
func (s *Store) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.issue_id, d.type FROM dependencies d WHERE d.depends_on_id = ?`, issueID)
	if err != nil {
		return nil, fmt.Errorf("get ephemeral dependents with metadata: %w", err)
	}
	defer rows.Close()

	type depMeta struct{ depID, depType string }
	var deps []depMeta
	for rows.Next() {
		var dm depMeta
		if err := rows.Scan(&dm.depID, &dm.depType); err != nil {
			return nil, err
		}
		deps = append(deps, dm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return nil, nil
	}

	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// GetDependencyTree returns a dependency tree for visualization.
func (s *Store) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	visited := make(map[string]bool)
	return s.buildDependencyTree(ctx, issueID, 0, maxDepth, reverse, visited)
}

func (s *Store) buildDependencyTree(ctx context.Context, issueID string, depth, maxDepth int, reverse bool, visited map[string]bool) ([]*types.TreeNode, error) {
	if depth >= maxDepth || visited[issueID] {
		return nil, nil
	}
	visited[issueID] = true

	issue, err := s.GetIssue(ctx, issueID)
	if err != nil || issue == nil {
		return nil, err
	}

	var query string
	if reverse {
		query = "SELECT issue_id FROM dependencies WHERE depends_on_id = ?"
	} else {
		query = "SELECT depends_on_id FROM dependencies WHERE issue_id = ?"
	}

	rows, err := s.db.QueryContext(ctx, query, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var childIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		childIDs = append(childIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	node := &types.TreeNode{
		Issue: *issue,
		Depth: depth,
	}

	nodes := []*types.TreeNode{node}
	for _, childID := range childIDs {
		children, err := s.buildDependencyTree(ctx, childID, depth+1, maxDepth, reverse, visited)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, children...)
	}

	return nodes, nil
}

// scanIssues scans multiple issues from rows.
func scanIssues(rows *sql.Rows) ([]*types.Issue, error) {
	var issues []*types.Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}
