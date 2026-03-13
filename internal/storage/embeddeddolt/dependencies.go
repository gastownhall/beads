//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	isCrossPrefix := types.ExtractPrefix(dep.IssueID) != types.ExtractPrefix(dep.DependsOnID)

	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		// Validate source issue exists and get its type.
		var sourceType string
		if err := tx.QueryRowContext(ctx, `SELECT issue_type FROM issues WHERE id = ?`, dep.IssueID).Scan(&sourceType); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("issue %s not found", dep.IssueID)
			}
			return fmt.Errorf("failed to check issue existence: %w", err)
		}

		// Validate target issue exists (skip for external and cross-prefix refs).
		var targetType string
		if !strings.HasPrefix(dep.DependsOnID, "external:") && !isCrossPrefix {
			if err := tx.QueryRowContext(ctx, `SELECT issue_type FROM issues WHERE id = ?`, dep.DependsOnID).Scan(&targetType); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("issue %s not found", dep.DependsOnID)
				}
				return fmt.Errorf("failed to check target issue existence: %w", err)
			}
		}

		// Cross-type blocking validation (GH#1495).
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

		// Cycle detection for blocking deps.
		if dep.Type == types.DepBlocks {
			var reachable int
			if err := tx.QueryRowContext(ctx, `
				WITH RECURSIVE reachable AS (
					SELECT ? AS node, 0 AS depth
					UNION ALL
					SELECT d.depends_on_id, r.depth + 1
					FROM reachable r
					JOIN dependencies d ON d.issue_id = r.node
					WHERE r.depth < 100
				)
				SELECT COUNT(*) FROM reachable WHERE node = ?
			`, dep.DependsOnID, dep.IssueID).Scan(&reachable); err != nil {
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
	})
}
