package sqlbuild

import (
	"fmt"
	"strings"
)

// descendantWalkTargetCols are the three mutually-exclusive dependency target
// columns, in the precedence order DepTargetExpr resolves them.
var descendantWalkTargetCols = []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}

func descendantWalkTables(includeWisps bool) []string {
	if includeWisps {
		return []string{"dependencies", "wisp_dependencies"}
	}
	return []string{"dependencies"}
}

// DescendantWalkQuery builds the transitive parent-child descendant walk for
// rootID, as one recursive member per (dependency table, target column) pair.
//
// The shape is load-bearing. Wrapping the edge relation in a non-recursive
// `parent_edges` CTE (dependencies UNION ALL wisp_dependencies, COALESCE'd
// target) leaves Dolt unable to push the recursive join into the wrapper: it
// re-scans the whole materialized relation once per recursion row instead of
// probing idx_dep_type_issue. Joining a base table on a bare column makes an
// index lookup possible, and the per-column IS NULL guards reproduce
// DepTargetExpr's COALESCE precedence exactly.
//
// The join hints are load-bearing too. Which side of the recursive join Dolt
// makes the outer one depends on its table statistics: with none yet for a
// freshly loaded edge table (Dolt 2.2.4) it keeps the recursive working table
// on the inner side and scans the edge table per iteration, which measures no
// faster than the wrapper shape, then flips to index lookups once statistics
// exist. JOIN_ORDER(d,e) LOOKUP_JOIN(d,e) pins the frontier as the outer side
// with an index probe into the edge table on every member regardless of
// statistics; on MySQL the unknown LOOKUP_JOIN hint is ignored with a
// warning, and other dialects read the comment as a comment.
//
// Bind with DescendantWalkArgs; placeholder order follows the member order
// built here.
func DescendantWalkQuery(includeWisps bool) string {
	var anchors, recursive []string
	for _, table := range descendantWalkTables(includeWisps) {
		for i, col := range descendantWalkTargetCols {
			anchors = append(anchors, fmt.Sprintf(`
				SELECT issue_id, 1, CONCAT(',', ?, ',', issue_id, ',')
				FROM %s
				WHERE type = 'parent-child' AND %s = ?%s`,
				table, col, nullGuards("", descendantWalkTargetCols[:i])))

			recursive = append(recursive, fmt.Sprintf(`
				SELECT /*+ JOIN_ORDER(d,e) LOOKUP_JOIN(d,e) */ e.issue_id, d.depth + 1, CONCAT(d.path, e.issue_id, ',')
				FROM descendants d
				JOIN %s e ON e.%s = d.id AND e.type = 'parent-child'
				WHERE (? <= 0 OR d.depth < ?)%s
				  AND LOCATE(CONCAT(',', e.issue_id, ','), d.path) = 0`,
				table, col, nullGuards("e.", descendantWalkTargetCols[:i])))
		}
	}

	members := strings.Join(append(anchors, recursive...), "\n\t\t\t\tUNION ALL")
	return fmt.Sprintf(`
			WITH RECURSIVE
			descendants(id, depth, path) AS (%s
			)
			SELECT id, depth FROM descendants WHERE id <> ?
		`, members)
}

// DescendantWalkArgs binds DescendantWalkQuery(includeWisps): rootID twice per
// anchor, maxDepth twice per recursive member, then rootID for the tail filter.
func DescendantWalkArgs(rootID string, maxDepth int, includeWisps bool) []interface{} {
	members := len(descendantWalkTables(includeWisps)) * len(descendantWalkTargetCols)
	args := make([]interface{}, 0, 4*members+1)
	for i := 0; i < members; i++ {
		args = append(args, rootID, rootID)
	}
	for i := 0; i < members; i++ {
		args = append(args, maxDepth, maxDepth)
	}
	return append(args, rootID)
}

func nullGuards(prefix string, cols []string) string {
	var b strings.Builder
	for _, col := range cols {
		b.WriteString(" AND " + prefix + col + " IS NULL")
	}
	return b.String()
}
