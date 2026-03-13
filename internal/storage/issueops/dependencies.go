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
type AddDependencyOpts struct {
	// SourceTable is the table to validate the source issue exists in.
	// Defaults to "issues" if empty.
	SourceTable string
	// TargetTable is the table to validate the target issue exists in.
	// Defaults to "issues" if empty. Ignored when target validation is skipped.
	TargetTable string
	// DepTables are the tables to scan for cycle detection. The recursive CTE
	// UNIONs all of them. Defaults to ["dependencies"] if empty.
	DepTables []string
	// IsCrossPrefix is true when source and target have different prefixes,
	// meaning the target lives in another rig's database.
	IsCrossPrefix bool
}

// AddDependencyInTx validates and inserts a dependency within an existing
// transaction. It handles:
//   - Source/target existence validation
//   - Cross-type blocking validation (GH#1495)
//   - Cycle detection via recursive CTE
//   - Idempotent same-type updates (metadata only)
//   - Type conflict detection
//
// The caller is responsible for transaction lifecycle, dolt commits, and
// any cache invalidation.
func AddDependencyInTx(ctx context.Context, tx *sql.Tx, dep *types.Dependency, actor string, opts AddDependencyOpts) error {
	sourceTable := opts.SourceTable
	if sourceTable == "" {
		sourceTable = "issues"
	}
	targetTable := opts.TargetTable
	if targetTable == "" {
		targetTable = "issues"
	}
	depTables := opts.DepTables
	if len(depTables) == 0 {
		depTables = []string{"dependencies"}
	}

	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	// Validate source issue exists and get its type.
	var sourceType string
	//nolint:gosec // G201: sourceTable is caller-controlled ("issues" or "wisps")
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT issue_type FROM %s WHERE id = ?`, sourceTable), dep.IssueID).Scan(&sourceType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("issue %s not found", dep.IssueID)
		}
		return fmt.Errorf("failed to check issue existence: %w", err)
	}

	// Validate target issue exists (skip for external and cross-prefix refs).
	var targetType string
	if !strings.HasPrefix(dep.DependsOnID, "external:") && !opts.IsCrossPrefix {
		//nolint:gosec // G201: targetTable is caller-controlled ("issues" or "wisps")
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
	err := tx.QueryRowContext(ctx, `SELECT type FROM dependencies WHERE issue_id = ? AND depends_on_id = ?`,
		dep.IssueID, dep.DependsOnID).Scan(&existingType)
	if err == nil {
		if existingType == string(dep.Type) {
			// Same type — idempotent; update metadata.
			if _, err := tx.ExecContext(ctx, `UPDATE dependencies SET metadata = ? WHERE issue_id = ? AND depends_on_id = ?`,
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

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}
	return nil
}
