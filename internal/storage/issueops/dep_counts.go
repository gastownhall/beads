package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
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
		n, err := countDistinctEdges(ctx, tx, "issue_id = ?", DepTargetExpr, DepTargetExpr+" IS NOT NULL", typeClause, sourceID, typeArgs)
		if err != nil {
			return 0, err
		}
		total += n
	}
	if dir == domain.DepDirectionIn || dir == domain.DepDirectionBoth {
		n, err := countInboundDistinctEdges(ctx, tx, sourceID, typeClause, typeArgs)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// countInboundDistinctEdges is the inbound leg of CountDependencyEdgesInTx. The
// target predicate cannot stay a COALESCE equality — no index matches the
// wrapper — so it is split per typed target column into guarded arms
// (sqlbuild.DepTargetArmsFor, gascity#7666); each arm probes the column's own
// target index, and the earlier-column IS NULL guards keep the arms disjoint,
// so the outer SUM returns exactly what one COALESCE-equality count returned.
//
// The wisp leg keeps the durable-first far-end rule: a wisp row whose source
// the durable plane already holds an edge for — under ANY target kind — is
// the same edge, not a second one. The exclusion therefore spans all three
// arms and is unfiltered by dependency type, exactly as the single-probe
// form's NOT IN subquery was.
func countInboundDistinctEdges(ctx context.Context, tx DBTX, sourceID, typeClause string, typeArgs []any) (int64, error) {
	arms := sqlbuild.DepTargetArmsFor("", "= ?")

	// Durable plane: one guarded arm per typed column, one COUNT each, summed
	// by the outer aggregate. Each WHERE binds the target once plus the type
	// filter.
	durableSQL := sqlbuild.DepTargetArmsSQL(arms, func(arm sqlbuild.DepTargetArm) string {
		return fmt.Sprintf("SELECT COUNT(*) AS n FROM dependencies WHERE %s%s", arm.Where, armsSuffix(typeClause))
	})
	durableArgs := make([]any, 0, len(arms)*(1+len(typeArgs)))
	for range arms {
		durableArgs = append(durableArgs, sourceID)
		durableArgs = append(durableArgs, typeArgs...)
	}
	durable, err := countArmsSum(ctx, tx, "dependencies", durableSQL, durableArgs)
	if err != nil {
		return 0, err
	}

	// The durable-far subquery each wisp arm excludes: the durable rows whose
	// target resolves to sourceID, per arm, unnamed by type.
	durableFarSQL := sqlbuild.DepTargetArmsSQL(arms, func(arm sqlbuild.DepTargetArm) string {
		return fmt.Sprintf("SELECT issue_id FROM dependencies WHERE %s", arm.Where)
	})
	wispSQL := sqlbuild.DepTargetArmsSQL(arms, func(arm sqlbuild.DepTargetArm) string {
		return fmt.Sprintf("SELECT COUNT(*) AS n FROM wisp_dependencies WHERE %s AND issue_id NOT IN (%s)%s",
			arm.Where, durableFarSQL, armsSuffix(typeClause))
	})
	wispArgs := make([]any, 0, len(arms)*(4+len(typeArgs)))
	for range arms {
		wispArgs = append(wispArgs, sourceID) // this arm's target predicate
		wispArgs = append(wispArgs, sourceID, sourceID, sourceID)
		wispArgs = append(wispArgs, typeArgs...)
	}
	wisp, err := countArmsSum(ctx, tx, "wisp_dependencies", wispSQL, wispArgs)
	if err != nil {
		if optionalBlockedTable("wisp_dependencies") && dberrors.IsMissingTable(err, "wisp_dependencies") {
			return durable, nil
		}
		return 0, err
	}
	return durable + wisp, nil
}

func armsSuffix(typeClause string) string {
	if typeClause == "" {
		return ""
	}
	return " AND " + typeClause
}

// countArmsSum sums a UNION ALL of per-arm COUNT(*) subqueries, tolerating an
// empty arms relation (COALESCE(SUM(n), 0), the same zero the single count(*)
// returned for an empty table).
func countArmsSum(ctx context.Context, tx DBTX, table, armsSQL string, args []any) (int64, error) {
	q := fmt.Sprintf("SELECT COALESCE(SUM(n), 0) FROM (%s) target_arms", armsSQL) //nolint:gosec // fixed fragments and ? placeholders only
	var n int64
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

// CountDependentsByStatusInTx counts issueID's dependents whose source row is
// in status, across both planes joined to each source's home table
// (dependencies→issues, wisp_dependencies→wisps). Both stores and the unit-of-
// work path share it; the target predicate is the same guarded-arms split as
// countInboundDistinctEdges (sqlbuild.DepTargetArmsFor, gascity#7666) in place
// of the COALESCE equality both stores ran separately before.
func CountDependentsByStatusInTx(ctx context.Context, tx DBTX, issueID, status string) (int64, error) {
	arms := sqlbuild.DepTargetArmsFor("d", "= ?")
	var total int64
	for _, plane := range edgeCountPlanes {
		armsSQL := sqlbuild.DepTargetArmsSQL(arms, func(arm sqlbuild.DepTargetArm) string {
			//nolint:gosec // G201: plane names come from the hardcoded edgeCountPlanes list.
			return fmt.Sprintf("SELECT COUNT(*) AS n FROM %s d JOIN %s s ON s.id = d.issue_id WHERE %s AND s.status = ?",
				plane.dependencies, plane.sources, arm.Where)
		})
		args := make([]any, 0, len(arms)*2)
		for range arms {
			args = append(args, issueID, status)
		}
		n, err := countArmsSum(ctx, tx, plane.dependencies, armsSQL, args)
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
// where a single NULL would make every comparison unknown.
func countDistinctEdges(ctx context.Context, tx DBTX, anchorExpr, farCol, farGuard, typeClause, sourceID string, typeArgs []any) (int64, error) {
	durable, err := countDepWhere(ctx, tx, "dependencies", anchorExpr, typeClause, []any{sourceID}, typeArgs)
	if err != nil {
		return 0, err
	}
	durableFar := fmt.Sprintf("SELECT %s FROM dependencies WHERE %s", farCol, anchorExpr) //nolint:gosec // fixed fragments
	if farGuard != "" {
		durableFar += " AND " + farGuard
	}
	wispWhere := fmt.Sprintf("%s AND %s NOT IN (%s)", anchorExpr, farCol, durableFar)
	wisp, err := countDepWhere(ctx, tx, "wisp_dependencies", wispWhere, typeClause, []any{sourceID, sourceID}, typeArgs)
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
