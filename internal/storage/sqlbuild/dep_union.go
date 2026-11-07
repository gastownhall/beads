package sqlbuild

import (
	"fmt"
	"strings"
)

// DepTargetUnion renders one SELECT branch per dependency target column, in
// DepTargetExpr's COALESCE precedence order, joined by UNION ALL: branch i
// matches its column bare and requires every earlier target column IS NULL,
// so at most one branch matches a row and the union reproduces the COALESCE
// resolution while every target predicate stays a bare column an index can
// match. Filtering through `COALESCE(...) = ?` instead defeats every target
// index and scans the whole table — the reason DescendantWalkQuery (same
// guarded shape) and issueops's inbound counts (this renderer) exist
// (gascity#7666).
//
// The guards are load-bearing even though ck_dep_one_target already holds
// exactly one non-NULL target per row: without them, a branch-2 match on
// depends_on_wisp_id would also count rows whose depends_on_issue_id is set,
// double-counting on any fixture or engine that does not enforce the CHECK.
//
// colPrefix qualifies the target columns when the branches alias the table
// ("" or "d."). from is the table plus any joins every branch shares
// ("dependencies" or "dependencies d JOIN issues i ON i.id = d.issue_id").
// projection is the branches' SELECT list; predicate is the target-key
// comparison with its placeholders ("= ?" or "IN (?, ?)"); extra is appended
// inside every branch's WHERE for filters that must repeat per branch (""
// or " AND i.status = ?"). Every fragment is hardcoded internal SQL —
// nothing user-controlled reaches this renderer. Bind with
// DepTargetUnionArgs; placeholder order follows the branch order built here.
func DepTargetUnion(colPrefix, from, projection, predicate, extra string) string {
	branches := make([]string, len(depTargetCols))
	for i, col := range depTargetCols {
		branches[i] = fmt.Sprintf("SELECT %s FROM %s WHERE %s%s %s%s%s",
			projection, from, colPrefix, col, predicate,
			nullGuards(colPrefix, depTargetCols[:i]), extra)
	}
	return strings.Join(branches, "\nUNION ALL ")
}

// DepTargetUnionArgs assembles the bindings for a DepTargetUnion query:
// perBranch is called once per branch to append that branch's placeholders
// in SQL text order — the branch's target keys first, then its extra
// fragment's — for all three branches in turn.
func DepTargetUnionArgs(perBranch func(args []any) []any) []any {
	args := make([]any, 0, 3*4)
	for range depTargetCols {
		args = perBranch(args)
	}
	return args
}
