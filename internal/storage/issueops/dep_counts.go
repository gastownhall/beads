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
		n, err := countDistinctEdges(ctx, tx, []string{"issue_id = ?"}, DepTargetExpr, DepTargetExpr+" IS NOT NULL", typeClause, sourceID, typeArgs)
		if err != nil {
			return 0, err
		}
		total += n
	}
	if dir == domain.DepDirectionIn || dir == domain.DepDirectionBoth {
		n, err := countDistinctEdges(ctx, tx, depTargetArmWheres("", "= ?"), "issue_id", "", typeClause, sourceID, typeArgs)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// countDistinctEdges counts the durable rows matching any of anchorArms, plus
// the wisp rows matching one whose farCol the durable plane does not already
// hold for the same anchor. anchorArms are disjoint predicates that each bind
// sourceID once: issue_id for an outbound count, one per target column
// (sqlbuild.DepTargetArms) for an inbound one, so every arm and every
// exclusion seeks an index where a COALESCE over the target columns would scan.
// farGuard, when set, keeps NULL far ends out of the NOT IN lists, where a
// single NULL would make every comparison unknown.
func countDistinctEdges(ctx context.Context, tx DBTX, anchorArms []string, farCol, farGuard, typeClause, sourceID string, typeArgs []any) (int64, error) {
	durable, err := countDepArms(ctx, tx, "dependencies", anchorArms, typeClause, []any{sourceID}, typeArgs)
	if err != nil {
		return 0, err
	}
	// x NOT IN (A UNION ALL B) is x NOT IN A AND x NOT IN B: one exclusion per
	// durable arm keeps each a seek.
	exclusions := make([]string, len(anchorArms))
	wispArgs := []any{sourceID}
	for i, arm := range anchorArms {
		durableFar := fmt.Sprintf("SELECT %s FROM dependencies WHERE %s", farCol, arm) //nolint:gosec // fixed fragments
		if farGuard != "" {
			durableFar += " AND " + farGuard
		}
		exclusions[i] = fmt.Sprintf("%s NOT IN (%s)", farCol, durableFar)
		wispArgs = append(wispArgs, sourceID)
	}
	wispArms := make([]string, len(anchorArms))
	for i, arm := range anchorArms {
		wispArms[i] = arm + " AND " + strings.Join(exclusions, " AND ")
	}
	wisp, err := countDepArms(ctx, tx, "wisp_dependencies", wispArms, typeClause, wispArgs, typeArgs)
	if err != nil {
		if optionalBlockedTable("wisp_dependencies") && dberrors.IsMissingTable(err, "wisp_dependencies") {
			return durable, nil
		}
		return 0, err
	}
	return durable + wisp, nil
}

// depTargetArmWheres is sqlbuild.DepTargetArms's predicates alone.
func depTargetArmWheres(prefix, cond string) []string {
	arms := sqlbuild.DepTargetArms(prefix, cond)
	wheres := make([]string, len(arms))
	for i, arm := range arms {
		wheres[i] = arm.Where
	}
	return wheres
}

// CountDependentsByStatusInTx returns the number of issues that depend on
// issueID and are in the given status: the body behind both stores'
// CountDependentsByStatus. It counts both dependency tables, joining each to
// its home issue table (dependencies→issues, wisp_dependencies→wisps), so wisp
// dependents are included the same way GetDependentsWithMetadata includes
// them, one count per plane summed in Go. Each matches the target one split
// physical column at a time (sqlbuild.DepTargetArms), never the STORED
// generated depends_on_id, which a count(*) can fail to resolve under the
// pure-Go GMS analyzer, nor their COALESCE, which no index can serve. The
// arms select the dependents' ids alone and join the status outside them: a
// join inside an arm lets Dolt merge-join the whole dependency index against
// the issue table instead of seeking the target.
func CountDependentsByStatusInTx(ctx context.Context, tx DBTX, issueID string, status types.Status) (int64, error) {
	var total int64
	for _, plane := range edgeCountPlanes {
		var selects []string
		var args []any
		for _, where := range depTargetArmWheres("", "= ?") {
			selects = append(selects, fmt.Sprintf("SELECT issue_id FROM %s WHERE %s", plane.dependencies, where))
			args = append(args, issueID)
		}
		args = append(args, string(status))
		var n int64
		//nolint:gosec // G201: table names are hardcoded literals; values ride ? placeholders.
		q := fmt.Sprintf("SELECT count(*) FROM (%s) d JOIN %s s ON s.id = d.issue_id WHERE s.status = ?", strings.Join(selects, " UNION ALL "), plane.sources)
		if err := tx.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// countDepArms counts table's rows matching any of the disjoint arms (and
// typeClause, when set), binding whereArgs then typeArgs once per arm. One arm
// is a plain count; several are a UNION ALL of one SELECT per arm counted
// outside, so each arm plans on its own index.
func countDepArms(ctx context.Context, tx DBTX, table string, arms []string, typeClause string, whereArgs, typeArgs []any) (int64, error) {
	selects := make([]string, len(arms))
	var args []any
	for i, arm := range arms {
		if typeClause != "" {
			arm += " AND " + typeClause
		}
		selects[i] = fmt.Sprintf("SELECT 1 FROM %s WHERE %s", table, arm) //nolint:gosec
		args = append(append(args, whereArgs...), typeArgs...)
	}
	q := "SELECT count(*) FROM (" + strings.Join(selects, " UNION ALL ") + ") edges"
	if len(arms) == 1 {
		q = "SELECT count(*) FROM " + strings.TrimPrefix(selects[0], "SELECT 1 FROM ")
	}
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
