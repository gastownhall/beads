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
//
// The inbound (In) direction counts through sqlbuild.DepTargetUnion — one
// guarded branch per target column — so each predicate matches a bare indexed
// column instead of the COALESCE expression no index can match (gascity#7666);
// the outbound direction anchors on issue_id, which is already sargable.
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

// countInboundDistinctEdges counts the durable rows whose target resolves to
// sourceID, plus the wisp rows that do while the durable plane does not hold
// the same (source, target) edge — the same arithmetic countDistinctEdges
// computed, re-expressed as two guarded per-column unions
// (sqlbuild.DepTargetUnion) so every target predicate can use the
// idx_dep_*_target indexes instead of scanning (gascity#7666).
//
// The wisp union's exclusion subquery projects issue_id from the UNFILTERED
// durable union: durable ownership must suppress the wisp copy of an edge
// whatever type the durable row carries, so the requested typeFilter must not
// narrow the subquery. Exactly two database calls, like before: one durable
// count, one wisp count.
func countInboundDistinctEdges(ctx context.Context, tx DBTX, sourceID string, typeClause string, typeArgs []any) (int64, error) {
	extra := ""
	if typeClause != "" {
		extra = " AND " + typeClause
	}
	// Branch text order: the wisp branch's own target key, the three exclusion
	// subquery target keys, then the type filter.
	wispExtra := " AND issue_id NOT IN (" + sqlbuild.DepTargetUnion("", "dependencies", "issue_id", "= ?", "") + ")" + extra

	durable, err := countDepUnion(ctx, tx, "dependencies",
		sqlbuild.DepTargetUnion("", "dependencies", "1", "= ?", extra),
		sqlbuild.DepTargetUnionArgs(func(a []any) []any {
			return append(append(a, sourceID), typeArgs...)
		}))
	if err != nil {
		return 0, err
	}
	wisp, err := countDepUnion(ctx, tx, "wisp_dependencies",
		sqlbuild.DepTargetUnion("", "wisp_dependencies", "1", "= ?", wispExtra),
		sqlbuild.DepTargetUnionArgs(func(a []any) []any {
			a = append(a, sourceID, sourceID, sourceID, sourceID)
			return append(a, typeArgs...)
		}))
	if err != nil {
		if optionalBlockedTable("wisp_dependencies") && dberrors.IsMissingTable(err, "wisp_dependencies") {
			return durable, nil
		}
		return 0, err
	}
	return durable + wisp, nil
}

// countDepUnion counts the rows of one DepTargetUnion query. table names the
// plane for the error wrap (a hardcoded constant, same as countDepWhere).
func countDepUnion(ctx context.Context, tx DBTX, table, union string, args []any) (int64, error) {
	q := "SELECT count(*) FROM (" + union + ") AS edge_branches" //nolint:gosec // union is built from hardcoded fragments
	var n int64
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

// CountDependentsByStatusInTx counts sourceID's dependents whose home row
// carries status: one guarded per-column union per plane (the same shape
// countInboundDistinctEdges uses), each branch joining the plane's home table
// — dependencies→issues, wisp_dependencies→wisps — so the status filter rides
// a bare target column instead of the COALESCE expression Dolt cannot index
// (gascity#7666). Both stores' CountDependentsByStatus delegate here, keeping
// the SQL in one place. Unlike CountDependencyEdgesInTx there is no
// missing-plane tolerance and no durable-ownership suppression: the
// home-table join drops a wisp row whose issue_id has no wisps row on its
// own, and the two planes are simply summed.
//
// Each branch's projection carries JOIN_ORDER/LOOKUP_JOIN hints pinning the
// dependency table as the join driver and the home table as the primary-key
// lookup: without them the statistics-less embedded planner drives the join
// from an issues.status index scan (every open row, per branch) and probes
// dependencies per row — the scan shape this rewrite exists to remove.
// TestDependentCountPlansUseTargetIndexes pins the plan; on MySQL, JOIN_ORDER
// is honored and the unknown LOOKUP_JOIN hint is ignored, the same trade the
// reachability walks make.
func CountDependentsByStatusInTx(ctx context.Context, tx DBTX, sourceID string, status types.Status) (int64, error) {
	planes := []struct {
		table string
		union string
	}{
		{
			"dependencies",
			sqlbuild.DepTargetUnion("d.", "dependencies d JOIN issues i ON i.id = d.issue_id",
				"/*+ JOIN_ORDER(d,i) LOOKUP_JOIN(d,i) */ d.issue_id", "= ?", " AND i.status = ?"),
		},
		{
			"wisp_dependencies",
			sqlbuild.DepTargetUnion("d.", "wisp_dependencies d JOIN wisps w ON w.id = d.issue_id",
				"/*+ JOIN_ORDER(d,w) LOOKUP_JOIN(d,w) */ d.issue_id", "= ?", " AND w.status = ?"),
		},
	}
	var total int64
	for _, plane := range planes {
		n, err := countDepUnion(ctx, tx, plane.table, plane.union,
			sqlbuild.DepTargetUnionArgs(func(a []any) []any {
				return append(a, sourceID, string(status))
			}))
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
