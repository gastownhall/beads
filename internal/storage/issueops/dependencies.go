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
