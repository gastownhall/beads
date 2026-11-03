package sqlbuild

import (
	"fmt"
	"strings"
)

// depTargetCols are the three mutually-exclusive dependency target columns,
// in the precedence order DepTargetExpr resolves them. Shared by the
// descendant walk and the guarded-arm target splits below.
var depTargetCols = []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}

// DepTargetArm is one arm of the guarded disjoint UNION ALL split of a
// dependency-target predicate. Select is the arm's target column, qualified
// with the alias the caller's query uses (bare when the alias is empty), so a
// caller can project it as depends_on_id / dep_id / anchor. Where is the
// per-arm predicate: the caller's column predicate bound to this arm's
// column, guarded by IS NULL on every earlier column so the arms partition
// rows exactly the way DepTargetExpr's COALESCE resolves them.
type DepTargetArm struct {
	Select string
	Where  string
}

// DepTargetArmsFor splits a dependency-target predicate over the three typed
// target columns into guarded disjoint arms (gascity#7666). colPred is the
// per-column predicate text — "= ?" for a single target, "IN (?,?)" for an id
// page — and alias qualifies every column with the caller's table alias
// ("" for none). Each arm binds colPred's placeholders once.
//
// The COALESCE-wrapper form this replaces binds an expression no index can
// match, so the engine scans the whole table for every count and lookup that
// does not know the target kind ahead of time. One arm per column is
// sargable — each WHERE probes the column's own target index — and the
// earlier-column IS NULL guards keep the arms disjoint, so a UNION ALL over
// them (under an outer SUM/COUNT when the caller wants one number) returns
// exactly what the COALESCE form counted, precedence order included: a row
// carrying more than one typed target counts once, under its first non-null
// column. Rows whose target column is the arm's but which the caller's
// predicate rejects fall out per arm, as they did under the COALESCE form.
//
// The caller repeats its bound args once per arm, in arm order; the arms are
// generated in depTargetCols order (issue, wisp, external).
func DepTargetArmsFor(alias, colPred string) []DepTargetArm {
	qual := func(col string) string {
		if alias == "" {
			return col
		}
		return alias + "." + col
	}
	arms := make([]DepTargetArm, 0, len(depTargetCols))
	var earlier []string
	for _, col := range depTargetCols {
		q := qual(col)
		where := fmt.Sprintf("%s %s", q, colPred)
		for _, prior := range earlier {
			where += " AND " + prior + " IS NULL"
		}
		arms = append(arms, DepTargetArm{Select: q, Where: where})
		earlier = append(earlier, q)
	}
	return arms
}

// DepTargetArmsSQL renders arms joined by UNION ALL, one select text per arm
// from armSQL. Shared by the query builders that inline arms; the joiner is
// separate so a caller can also reach for a single arm's Where.
func DepTargetArmsSQL(arms []DepTargetArm, armSQL func(DepTargetArm) string) string {
	selects := make([]string, len(arms))
	for i, arm := range arms {
		selects[i] = armSQL(arm)
	}
	return strings.Join(selects, "\nUNION ALL\n")
}
