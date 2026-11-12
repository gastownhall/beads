package issueops

import (
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// FailureCloseReasonSQL renders types.IsFailureClose over a SQL text
// expression: true exactly when the lowercased reason contains one of
// types.FailureCloseKeywords, with NULL read as the empty reason (never a
// failure).
//
// It is generated from the Go keyword list rather than written out, so the two
// cannot drift. The spelling is INSTR, not LIKE: the batched recompute
// templates are expanded by counting their percent signs and question marks
// (expandBatchTemplate), so this text must carry neither. A keyword's own
// single quotes are doubled (won't fix). In the supported engine LOWER is
// strings.ToLower and INSTR is an exact rune match, which is what makes the
// result equal IsFailureClose; TestFailureCloseReasonSQLMatchesIsFailureClose
// (internal/storage/dolt) executes it to prove that.
//
// reasonExpr must be a trusted constant SQL expression (a column reference).
func FailureCloseReasonSQL(reasonExpr string) string {
	lowered := "LOWER(COALESCE(" + reasonExpr + ", ''))"
	clauses := make([]string, 0, len(types.FailureCloseKeywords))
	for _, keyword := range types.FailureCloseKeywords {
		clauses = append(clauses,
			"INSTR("+lowered+", '"+strings.ReplaceAll(keyword, "'", "''")+"') > 0")
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

// BlockingTargetHoldsSQL is the SQL spelling of types.BlockingEdgeHolds for a
// row already known to be a 'blocks' or 'conditional-blocks' edge: the target
// (statusExpr, reasonExpr) still holds its source back. A pinned target never
// does; an unclosed target always does; a closed one holds only a
// conditional-blocks edge, and only while its close reason is not a failure.
//
// All three arguments must be trusted constant SQL expressions.
func BlockingTargetHoldsSQL(depTypeExpr, statusExpr, reasonExpr string) string {
	return "(" + statusExpr + " <> 'pinned' AND (" + statusExpr + " <> 'closed' OR (" +
		depTypeExpr + " = 'conditional-blocks' AND NOT " + FailureCloseReasonSQL(reasonExpr) + ")))"
}

// DepTypeLiteralSQL renders a blocking dependency type as the SQL string
// literal BlockingTargetHoldsSQL compares against, for a writer that knows the
// edge's type in Go rather than reading it from a row (the two direct-insert
// writers). Only the two blocking types reach it; anything else renders as
// 'blocks'.
func DepTypeLiteralSQL(depType types.DependencyType) string {
	if depType == types.DepConditionalBlocks {
		return "'conditional-blocks'"
	}
	return "'blocks'"
}
