package mysqldialect

import (
	"fmt"
	"regexp"
	"strings"
)

// selfRefUpdateRe matches bd's is_blocked recompute shape:
//
//	UPDATE <table> <alias> SET <...> WHERE <alias>.id IN (<placeholders>) <rest>
//
// The SET is non-greedy up to the first WHERE (SET clauses contain no WHERE); the
// placeholder list is the batched `?,?,…`.
var selfRefUpdateRe = regexp.MustCompile(`(?is)^(\s*UPDATE\s+(\w+)\s+(\w+)\s+SET\s+.+?)\s+WHERE\s+(\w+)\.id\s+IN\s+\(([^)]*)\)\s*(.*)$`)

// rewriteSelfRefUpdate rewrites the is_blocked recompute UPDATE into a MySQL-8-safe
// double-nested form so the target table's self-reference is materialized (avoiding
// error 1093). Everything else is returned verbatim. The transform is semantics-
// preserving: the id set (batch ∩ conditions) is computed in a materialized derived
// table, and the outer UPDATE writes exactly those rows. Positional placeholders keep
// their order because the WHERE body is moved intact.
func rewriteSelfRefUpdate(q string) string {
	// Fast reject: only the is_blocked recompute both sets is_blocked and self-joins.
	if !strings.Contains(q, "is_blocked") {
		return q
	}
	m := selfRefUpdateRe.FindStringSubmatch(q)
	if m == nil {
		return q
	}
	head, table, alias, whereAlias, placeholders, rest := m[1], m[2], m[3], m[4], m[5], m[6]
	// Only the recompute has further conditions after the IN clause (the self-ref
	// EXISTS block); a plain `UPDATE … WHERE id IN (…)` needs no rewrite.
	if whereAlias != alias || strings.TrimSpace(rest) == "" {
		return q
	}
	// Rebind the outer alias to the derived-table alias inside the moved conditions.
	reb := regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\.`)
	restRebound := reb.ReplaceAllString(rest, "_b.")
	return fmt.Sprintf("%s WHERE %s.id IN (SELECT id FROM (SELECT _b.id FROM %s _b WHERE _b.id IN (%s) %s) AS _blk)",
		head, alias, table, placeholders, restRebound)
}
