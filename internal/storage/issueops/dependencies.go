package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// AddDependencyOpts configures AddDependencyInTx behavior.
// When fields are left empty, AddDependencyInTx performs wisp routing
// automatically via IsActiveWispInTx. Callers that have already determined
// routing (e.g., DoltStore with its pre-tx wisp cache) can set fields
// explicitly to skip the redundant DB check.
type AddDependencyOpts struct {
	// SourceTable is the table to validate the source issue exists in.
	// Auto-detected via wisp routing if empty.
	SourceTable string
	// TargetTable is the table to validate the target issue exists in.
	// Auto-detected via wisp routing if empty. Ignored when target validation is skipped.
	TargetTable string
	// WriteTable is the dependency table to insert/update/check existing deps in.
	// Auto-detected from source wisp routing if empty.
	WriteTable string
	// DepTables are the tables to scan for cycle detection. The recursive CTE
	// UNIONs all of them. Defaults to ["dependencies", "wisp_dependencies"] if empty.
	DepTables []string
	// IsCrossPrefix is true when source and target have different prefixes,
	// meaning the target lives in another rig's database.
	IsCrossPrefix bool
}

// AddDependencyInTx validates and inserts a dependency within an existing
// transaction. It handles:
//   - Wisp routing (auto-detected or caller-provided)
//   - Source/target existence validation
//   - Cross-type blocking validation (GH#1495)
//   - Cycle detection via recursive CTE across both dependency tables
//   - Idempotent same-type updates (metadata only)
//   - Type conflict detection
//
// The caller is responsible for transaction lifecycle, dolt commits, and
// any cache invalidation.
func AddDependencyInTx(ctx context.Context, tx *sql.Tx, dep *types.Dependency, actor string, opts AddDependencyOpts) error {
	// Auto-detect source routing if not provided.
	sourceTable := opts.SourceTable
	writeTable := opts.WriteTable
	if sourceTable == "" || writeTable == "" {
		sourceIsWisp := IsActiveWispInTx(ctx, tx, dep.IssueID)
		st, _, _, dt := WispTableRouting(sourceIsWisp)
		if sourceTable == "" {
			sourceTable = st
		}
		if writeTable == "" {
			writeTable = dt
		}
	}

	// Auto-detect target routing if not provided (skip for external/cross-prefix).
	targetTable := opts.TargetTable
	if targetTable == "" && !strings.HasPrefix(dep.DependsOnID, "external:") && !opts.IsCrossPrefix {
		targetIsWisp := IsActiveWispInTx(ctx, tx, dep.DependsOnID)
		targetTable, _, _, _ = WispTableRouting(targetIsWisp)
	}
	if targetTable == "" {
		targetTable = "issues"
	}

	depTables := opts.DepTables
	if len(depTables) == 0 {
		depTables = []string{"dependencies", "wisp_dependencies"}
	}

	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	// Validate source issue exists and get its type.
	var sourceType string
	//nolint:gosec // G201: sourceTable is from WispTableRouting ("issues" or "wisps")
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT issue_type FROM %s WHERE id = ?`, sourceTable), dep.IssueID).Scan(&sourceType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("issue %s not found", dep.IssueID)
		}
		return fmt.Errorf("failed to check issue existence: %w", err)
	}

	// Validate target issue exists (skip for external and cross-prefix refs).
	var targetType string
	if !strings.HasPrefix(dep.DependsOnID, "external:") && !opts.IsCrossPrefix {
		//nolint:gosec // G201: targetTable is from WispTableRouting ("issues" or "wisps")
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT issue_type FROM %s WHERE id = ?`, targetTable), dep.DependsOnID).Scan(&targetType); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("issue %s not found", dep.DependsOnID)
			}
			return fmt.Errorf("failed to check target issue existence: %w", err)
		}
	}

	// Cross-type blocking validation (GH#1495): tasks can only block tasks,
	// epics can only block epics.
	if dep.Type == types.DepBlocks && targetType != "" {
		sourceIsEpic := sourceType == string(types.TypeEpic)
		targetIsEpic := targetType == string(types.TypeEpic)
		if sourceIsEpic != targetIsEpic {
			if sourceIsEpic {
				return fmt.Errorf("epics can only block other epics, not tasks")
			}
			return fmt.Errorf("tasks can only block other tasks, not epics")
		}
	}

	// Cycle detection for blocking deps via recursive CTE.
	if dep.Type == types.DepBlocks {
		// Build UNION ALL across all dep tables for the CTE.
		var unions []string
		for _, t := range depTables {
			//nolint:gosec // G201: depTables are caller-controlled constants
			unions = append(unions, fmt.Sprintf("SELECT issue_id, depends_on_id FROM %s WHERE type = 'blocks'", t))
		}
		unionQuery := strings.Join(unions, " UNION ALL ")

		var reachable int
		//nolint:gosec // G201: unionQuery built from caller-controlled table names
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
			WITH RECURSIVE reachable AS (
				SELECT ? AS node, 0 AS depth
				UNION ALL
				SELECT d.depends_on_id, r.depth + 1
				FROM reachable r
				JOIN (%s) d ON d.issue_id = r.node
				WHERE r.depth < 100
			)
			SELECT COUNT(*) FROM reachable WHERE node = ?
		`, unionQuery), dep.DependsOnID, dep.IssueID).Scan(&reachable); err != nil {
			return fmt.Errorf("failed to check for dependency cycle: %w", err)
		}
		if reachable > 0 {
			return fmt.Errorf("adding dependency would create a cycle")
		}
	}

	// Check for existing dependency between the same pair.
	var existingType string
	//nolint:gosec // G201: writeTable is from WispTableRouting ("dependencies" or "wisp_dependencies")
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT type FROM %s WHERE issue_id = ? AND depends_on_id = ?`, writeTable),
		dep.IssueID, dep.DependsOnID).Scan(&existingType)
	if err == nil {
		if existingType == string(dep.Type) {
			// Same type — idempotent; update metadata.
			//nolint:gosec // G201: writeTable is from WispTableRouting
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET metadata = ? WHERE issue_id = ? AND depends_on_id = ?`, writeTable),
				metadata, dep.IssueID, dep.DependsOnID); err != nil {
				return fmt.Errorf("failed to update dependency metadata: %w", err)
			}
			return nil
		}
		return fmt.Errorf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
			dep.IssueID, dep.DependsOnID, existingType, dep.Type)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check existing dependency: %w", err)
	}

	//nolint:gosec // G201: writeTable is from WispTableRouting
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
	`, writeTable), dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}
	return nil
}

// RemoveDependencyInTx removes a dependency between two issues within an
// existing transaction. Automatically routes to wisp_dependencies if the
// source issue is an active wisp.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func RemoveDependencyInTx(ctx context.Context, tx *sql.Tx, issueID, dependsOnID string) error {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	_, _, _, depTable := WispTableRouting(isWisp)

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE issue_id = ? AND depends_on_id = ?`, depTable),
		issueID, dependsOnID); err != nil {
		return fmt.Errorf("remove dependency: %w", err)
	}
	return nil
}

// GetIssuesByIDsInTx retrieves multiple issues by ID within an existing
// transaction, including labels. Automatically routes each ID to the correct
// table (issues/wisps). Uses batched IN clauses.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GetIssuesByIDsInTx(ctx context.Context, tx *sql.Tx, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Partition IDs by wisp status.
	var wispIDs, permIDs []string
	for _, id := range ids {
		if IsActiveWispInTx(ctx, tx, id) {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}

	var allIssues []*types.Issue
	for _, pair := range []struct {
		table    string
		labelTbl string
		ids      []string
	}{
		{"issues", "labels", permIDs},
		{"wisps", "wisp_labels", wispIDs},
	} {
		if len(pair.ids) == 0 {
			continue
		}
		for start := 0; start < len(pair.ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(pair.ids) {
				end = len(pair.ids)
			}
			batch := pair.ids[start:end]

			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for i, id := range batch {
				placeholders[i] = "?"
				args[i] = id
			}
			inClause := strings.Join(placeholders, ",")

			rows, err := tx.QueryContext(ctx, fmt.Sprintf(
				`SELECT %s FROM %s WHERE id IN (%s)`,
				IssueSelectColumns, pair.table, inClause), args...)
			if err != nil {
				return nil, fmt.Errorf("get issues by IDs from %s: %w", pair.table, err)
			}
			issueMap := make(map[string]*types.Issue)
			for rows.Next() {
				issue, scanErr := ScanIssueFrom(rows)
				if scanErr != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("get issues by IDs: scan: %w", scanErr)
				}
				allIssues = append(allIssues, issue)
				issueMap[issue.ID] = issue
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("get issues by IDs: rows: %w", err)
			}

			// Hydrate labels.
			if len(issueMap) > 0 {
				labelRows, err := tx.QueryContext(ctx, fmt.Sprintf(
					`SELECT issue_id, label FROM %s WHERE issue_id IN (%s) ORDER BY issue_id, label`,
					pair.labelTbl, inClause), args...)
				if err != nil {
					return nil, fmt.Errorf("get issues by IDs: labels from %s: %w", pair.labelTbl, err)
				}
				for labelRows.Next() {
					var issueID, label string
					if scanErr := labelRows.Scan(&issueID, &label); scanErr != nil {
						_ = labelRows.Close()
						return nil, fmt.Errorf("get issues by IDs: scan label: %w", scanErr)
					}
					if issue, ok := issueMap[issueID]; ok {
						issue.Labels = append(issue.Labels, label)
					}
				}
				_ = labelRows.Close()
				if err := labelRows.Err(); err != nil {
					return nil, fmt.Errorf("get issues by IDs: label rows: %w", err)
				}
			}
		}
	}

	return allIssues, nil
}

// GetDependenciesWithMetadataInTx returns issues that the given issueID depends on,
// along with the dependency type. Works within an existing transaction.
// Queries both dependency tables to handle cross-table dependencies.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetDependenciesWithMetadataInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	type depMeta struct {
		depID, depType string
	}

	// Query both dependency tables to find all dependencies.
	var deps []depMeta
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT depends_on_id, type FROM %s WHERE issue_id = ?`, depTable), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependencies from %s: %w", depTable, err)
		}
		for rows.Next() {
			var d depMeta
			if scanErr := rows.Scan(&d.depID, &d.depType); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependencies: scan: %w", scanErr)
			}
			deps = append(deps, d)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependencies: rows from %s: %w", depTable, err)
		}
	}

	if len(deps) == 0 {
		return nil, nil
	}

	// Fetch all dependency target issues.
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("get dependencies: fetch issues: %w", err)
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

// GetDependentsWithMetadataInTx returns issues that depend on the given issueID
// along with the dependency type. Works within an existing transaction.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GetDependentsWithMetadataInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	type depMeta struct {
		depID, depType string
	}

	// Query both dependency tables to find all dependents.
	var deps []depMeta
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, type FROM %s WHERE depends_on_id = ?`, depTable), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependents from %s: %w", depTable, err)
		}
		for rows.Next() {
			var d depMeta
			if scanErr := rows.Scan(&d.depID, &d.depType); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependents: scan: %w", scanErr)
			}
			deps = append(deps, d)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependents: rows from %s: %w", depTable, err)
		}
	}

	if len(deps) == 0 {
		return nil, nil
	}

	// Fetch all dependent issues.
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("get dependents: fetch issues: %w", err)
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			// Cross-rig dependent: issue lives in a different database.
			// Return a stub so the dep is visible rather than silently
			// dropped (GH#2624).
			results = append(results, &types.IssueWithDependencyMetadata{
				Issue:          types.Issue{ID: d.depID},
				DependencyType: types.DependencyType(d.depType),
			})
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// GetDependenciesInTx returns issues that the given issueID depends on.
// Queries both dependencies and wisp_dependencies tables.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetDependenciesInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.Issue, error) {
	var ids []string
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT depends_on_id FROM %s WHERE issue_id = ?`, depTable), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependencies from %s: %w", depTable, err)
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependencies: scan: %w", scanErr)
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependencies: rows from %s: %w", depTable, err)
		}
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return GetIssuesByIDsInTx(ctx, tx, ids)
}

// GetDependentsInTx returns issues that depend on the given issueID.
// Queries both dependencies and wisp_dependencies tables.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetDependentsInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.Issue, error) {
	var ids []string
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id FROM %s WHERE depends_on_id = ?`, depTable), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependents from %s: %w", depTable, err)
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependents: scan: %w", scanErr)
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependents: rows from %s: %w", depTable, err)
		}
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return GetIssuesByIDsInTx(ctx, tx, ids)
}
