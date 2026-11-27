package sqlbuild

import (
	"fmt"
	"strings"
)

// DepTargetUnion renders a target-keyed read as one UNION ALL branch per
// dependency target column, in DepTargetExpr precedence order:
//
//	SELECT <project(col)> FROM <from>
//	WHERE <qualifier><col> <match> [AND COALESCE(<higher cols>) IS NULL]<filter>
//
// The branches together select exactly the rows a single
// `DepTargetExpr <match>` select would, row for row: the guard gives each row
// to the one branch whose column COALESCE would have returned, even in a table
// whose rows set more than one target column. What changes is the plan: each
// branch compares a bare column, which Dolt can turn into an index range
// bounded to the matched values, where the COALESCE expression leaves it
// nothing to seek (see DescendantWalkQuery for the same pattern in the walk).
//
// The guard is a COALESCE rather than per-column IS NULL tests so that it is
// not sargable itself: with no statistics for a table (a fresh
// wisp_dependencies, say) Dolt can otherwise pick a higher column's IS NULL
// range, which holds every row targeting another plane, over the matched
// column's own index.
//
// from (a table, optionally with an alias and joins), qualifier (the alias of
// the dependency table, with its dot, or empty), project (the select list,
// which may lead with an optimizer hint comment), match ("= ?" or
// "IN (?,...)") and filter (a leading " AND ..." or empty) are trusted
// internal fragments. Bind with DepTargetUnionArgs.
func DepTargetUnion(from, qualifier string, project func(col string) string, match, filter string) string {
	branches := make([]string, len(descendantWalkTargetCols))
	for i, col := range descendantWalkTargetCols {
		guard := ""
		if i > 0 {
			higher := make([]string, i)
			for j, h := range descendantWalkTargetCols[:i] {
				higher[j] = qualifier + h
			}
			guard = " AND COALESCE(" + strings.Join(higher, ", ") + ") IS NULL"
		}
		branches[i] = fmt.Sprintf("SELECT %s FROM %s WHERE %s%s %s%s%s",
			project(qualifier+col), from, qualifier, col, match, guard, filter)
	}
	return strings.Join(branches, " UNION ALL ")
}

// DepTargetUnionArgs binds DepTargetUnion: matchArgs then filterArgs, once per
// branch.
func DepTargetUnionArgs(matchArgs, filterArgs []any) []any {
	args := make([]any, 0, len(descendantWalkTargetCols)*(len(matchArgs)+len(filterArgs)))
	for range descendantWalkTargetCols {
		args = append(args, matchArgs...)
		args = append(args, filterArgs...)
	}
	return args
}
