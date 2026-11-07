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
		n, err := countInboundEdges(ctx, tx, typeClause, sourceID, typeArgs)
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

// countInboundEdges is countDistinctEdges for the In direction, where the
// anchor is the target. Each plane is read as sqlbuild.DepTargetUnion's guarded
// per-column branches rather than through DepTargetExpr, so every branch can
// seek a target-column index instead of scanning the table (GH#7666).
// The type filter sits inside each counted branch; the durable union that
// excludes wisp rows is unfiltered, as in countDistinctEdges.
func countInboundEdges(ctx context.Context, tx DBTX, typeClause, targetID string, typeArgs []any) (int64, error) {
	filter := ""
	if typeClause != "" {
		filter = " AND " + typeClause
	}
	sources := func(table, filter string) string {
		return sqlbuild.DepTargetUnion(table, "", func(string) string { return "issue_id" }, "= ?", filter)
	}
	target := []any{targetID}

	var durable int64
	q := fmt.Sprintf("SELECT count(*) FROM (%s) durable_sources", sources("dependencies", filter)) //nolint:gosec // fixed fragments
	if err := tx.QueryRowContext(ctx, q, sqlbuild.DepTargetUnionArgs(target, typeArgs)...).Scan(&durable); err != nil {
		return 0, fmt.Errorf("count dependencies: %w", err)
	}

	var wisp int64
	q = fmt.Sprintf("SELECT count(*) FROM (%s) wisp_sources WHERE issue_id NOT IN (%s)", //nolint:gosec // fixed fragments
		sources("wisp_dependencies", filter), sources("dependencies", ""))
	args := append(sqlbuild.DepTargetUnionArgs(target, typeArgs), sqlbuild.DepTargetUnionArgs(target, nil)...)
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&wisp); err != nil {
		if optionalBlockedTable("wisp_dependencies") && dberrors.IsMissingTable(err, "wisp_dependencies") {
			return durable, nil
		}
		return 0, fmt.Errorf("count wisp_dependencies: %w", err)
	}
	return durable + wisp, nil
}

// CountDependentsByStatusInTx counts the issues that depend on issueID and are
// in status, across both planes: dependencies joined to issues and
// wisp_dependencies joined to wisps, summed, so a wisp dependent counts the way
// GetDependentsWithMetadata lists it. Each plane is one query over
// sqlbuild.DepTargetUnion's guarded per-column branches, so the target match
// can seek an index instead of scanning the edge table (GH#7666). The
// join hints keep the edge table the outer side, probing the home table by
// primary key per matched edge; without them Dolt drives the join from a
// status index range or a merge join over the whole edge table. A missing
// wisp plane is an error here, as it always was for this count.
func CountDependentsByStatusInTx(ctx context.Context, tx DBTX, issueID string, status types.Status) (int64, error) {
	var total int64
	for _, plane := range []struct{ deps, home string }{
		{"dependencies", "issues"},
		{"wisp_dependencies", "wisps"},
	} {
		union := sqlbuild.DepTargetUnion(plane.deps+" d JOIN "+plane.home+" h ON h.id = d.issue_id", "d.",
			func(string) string { return "/*+ JOIN_ORDER(d,h) LOOKUP_JOIN(d,h) */ d.issue_id" }, "= ?", " AND h.status = ?")
		q := fmt.Sprintf("SELECT count(*) FROM (%s) dependents", union) //nolint:gosec // fixed fragments
		var n int64
		if err := tx.QueryRowContext(ctx, q, sqlbuild.DepTargetUnionArgs([]any{issueID}, []any{string(status)})...).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
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
