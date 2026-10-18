package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// CountDependencyEdgesInTx counts sourceID's edges in direction dir: one per
// distinct far end, the same entries the dependency listings return
// (anchorEdgesInTx). An edge stored in both `dependencies` and
// `wisp_dependencies` counts once, and a database with no `wisp_dependencies`
// (a dolt-ignored table a working-set reset can drop) counts the durable plane
// alone. A count above the listing makes `bd show` warn about edges "whose far
// end has no row" and publish an inflated dependent_count (bd-cif6y4).
//
// typeFilter narrows the counted rows only. The wisp leg's NOT IN subquery is
// unfiltered, so a far end the durable plane owns under another type is not
// counted again under the wisp row's type: the durable row is the edge.
func CountDependencyEdgesInTx(ctx context.Context, tx DBTX, sourceID string, dir domain.DepDirection, typeFilter []types.DependencyType) (int64, error) {
	typeClause, typeArgs := buildDepTypeClause(typeFilter)

	var total int64
	if dir == domain.DepDirectionOut || dir == domain.DepDirectionBoth {
		n, err := countDistinctEdges(ctx, tx, "issue_id = ?", DepTargetExpr, DepTargetExpr+" IS NOT NULL", typeClause, []any{sourceID}, typeArgs)
		if err != nil {
			return 0, err
		}
		total += n
	}
	if dir == domain.DepDirectionIn || dir == domain.DepDirectionBoth {
		// depTargetEqualsOr, not DepTargetExpr+" = ?": the COALESCE form
		// wraps the three target columns in an expression no index can
		// match, so this count degraded into a full table scan of
		// dependencies/wisp_dependencies (GH#7666). depTargetEqualsOr is
		// the existing SARGABLE per-column-OR predicate already used by
		// queryDependentRecordsFromTable and CountDependentRecordsInTx
		// for the same reason — this just wires it into the one count
		// path that still used the COALESCE form. sourceID repeats once
		// per OR'd column (depTargetEqualsOr's own doc comment).
		n, err := countDistinctEdges(ctx, tx, depTargetEqualsOr(), "issue_id", "", typeClause, []any{sourceID, sourceID, sourceID}, typeArgs)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// countDistinctEdges counts the durable rows matching anchorExpr, plus the wisp
// rows matching it whose farCol the durable plane does not already hold for the
// same anchor. farGuard, when set, keeps NULL far ends out of the NOT IN list,
// where a single NULL would make every comparison unknown. anchorArgs binds
// anchorExpr's own placeholders (once for the durable count; doubled for the
// wisp count, since wispWhere embeds anchorExpr twice — once directly, once
// inside durableFar's nested subquery).
func countDistinctEdges(ctx context.Context, tx DBTX, anchorExpr, farCol, farGuard, typeClause string, anchorArgs []any, typeArgs []any) (int64, error) {
	durable, err := countDepWhere(ctx, tx, "dependencies", anchorExpr, typeClause, anchorArgs, typeArgs)
	if err != nil {
		return 0, err
	}
	durableFar := fmt.Sprintf("SELECT %s FROM dependencies WHERE %s", farCol, anchorExpr) //nolint:gosec // fixed fragments
	if farGuard != "" {
		durableFar += " AND " + farGuard
	}
	wispWhere := fmt.Sprintf("%s AND %s NOT IN (%s)", anchorExpr, farCol, durableFar)
	wispArgs := append(append([]any{}, anchorArgs...), anchorArgs...)
	wisp, err := countDepWhere(ctx, tx, "wisp_dependencies", wispWhere, typeClause, wispArgs, typeArgs)
	if err != nil {
		if optionalBlockedTable("wisp_dependencies") && dberrors.IsMissingTable(err, "wisp_dependencies") {
			return durable, nil
		}
		return 0, err
	}
	return durable + wisp, nil
}

func countDepWhere(ctx context.Context, tx DBTX, table, whereExpr, typeClause string, whereArgs, typeArgs []any) (int64, error) {
	q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", table, whereExpr) //nolint:gosec
	if typeClause != "" {
		q += " AND " + typeClause
	}
	args := append(append([]any{}, whereArgs...), typeArgs...)
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
