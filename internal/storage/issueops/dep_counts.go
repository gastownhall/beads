package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func CountDependencyEdgesInTx(ctx context.Context, tx DBTX, sourceID string, dir domain.DepDirection, typeFilter []types.DependencyType) (int64, error) {
	typeClause, typeArgs := buildDepTypeClause(typeFilter)

	var total int64
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		if dir == domain.DepDirectionOut || dir == domain.DepDirectionBoth {
			n, err := countDepWhere(ctx, tx, table, "issue_id = ?", typeClause, sourceID, typeArgs)
			if err != nil {
				return 0, err
			}
			total += n
		}
		if dir == domain.DepDirectionIn || dir == domain.DepDirectionBoth {
			n, err := countDepWhere(ctx, tx, table, DepTargetExpr+" = ?", typeClause, sourceID, typeArgs)
			if err != nil {
				return 0, err
			}
			total += n
		}
	}
	return total, nil
}

func countDepWhere(ctx context.Context, tx DBTX, table, whereExpr, typeClause string, sourceID string, typeArgs []any) (int64, error) {
	q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", table, whereExpr) //nolint:gosec
	if typeClause != "" {
		q += " AND " + typeClause
	}
	args := append([]any{sourceID}, typeArgs...)
	var n int64
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

func buildDepTypeClause(filter []types.DependencyType) (string, []any) {
	if len(filter) == 0 {
		return "", nil
	}
	placeholders := make([]string, len(filter))
	args := make([]any, len(filter))
	for i, t := range filter {
		placeholders[i] = "?"
		args[i] = string(t)
	}
	return "type IN (" + strings.Join(placeholders, ",") + ")", args
}
