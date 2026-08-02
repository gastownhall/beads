package sqlbuild

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// BuildReclaimScopeClauses renders the WHERE fragments that scope a reclaim to
// a subset of the stale-lease set. It returns only the scoping predicates: the
// staleness predicate itself (status/lease_expires_at) belongs to the reclaim
// query and is not this builder's business.
//
// alias is the SQL alias the reclaim query binds the issues table to (e.g. "i"
// in `... FROM leases l JOIN issues i ON i.id = l.issue_id`); the id/assignee
// column references are qualified with it so the clauses compose with the
// leases JOIN without column ambiguity. Pass "" for an unaliased issues table.
//
// The label clauses are deliberately the same SQL shapes the claim/ready path
// emits (AND-set per-label subquery, OR-set IN-list, exclusion NOT IN), so a
// fleet that partitions its claims by label can partition its reclaims
// identically and get the same matching semantics. All fields AND-combine; the
// zero filter yields no clauses at all.
//
// FAIL-CLOSED on a degenerate field: a field that carries entries which are ALL
// empty strings renders "1=0" (match nothing) rather than dropping out. Dropping
// out is how `bd reclaim --label "$LANE"` with LANE unset would silently become
// a GLOBAL sweep — the precise accident scoping exists to prevent. Reclaiming
// nothing is recoverable; reclaiming the whole federation is not. (The CLI
// rejects such a flag outright; this is the belt to that suspenders, covering
// programmatic callers.)
func BuildReclaimScopeClauses(filter types.ReclaimFilter, tables FilterTables, alias string) ([]string, []any) {
	var clauses []string
	var args []any

	// col qualifies a column of the issues table with the query's alias.
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}

	// degenerate reports a field that was populated but has no usable values.
	degenerate := func(raw, compacted []string) bool { return len(raw) > 0 && len(compacted) == 0 }

	ids := CompactNonEmptyStrings(filter.IDs)
	switch {
	case degenerate(filter.IDs, ids):
		clauses = append(clauses, "1=0")
	case len(ids) > 0:
		ph, idArgs := InPlaceholders(ids)
		clauses = append(clauses, fmt.Sprintf("%s IN (%s)", col("id"), ph))
		args = append(args, idArgs...)
	}

	assignees := CompactNonEmptyStrings(filter.Assignees)
	switch {
	case degenerate(filter.Assignees, assignees):
		clauses = append(clauses, "1=0")
	case len(assignees) > 0:
		ph, aArgs := InPlaceholders(assignees)
		clauses = append(clauses, fmt.Sprintf("%s IN (%s)", col("assignee"), ph))
		args = append(args, aArgs...)
	}

	labels := CompactNonEmptyStrings(filter.Labels)
	if degenerate(filter.Labels, labels) {
		clauses = append(clauses, "1=0")
	}
	for _, label := range labels {
		clauses = append(clauses, fmt.Sprintf("%s IN (SELECT issue_id FROM %s WHERE label = ?)", col("id"), tables.Labels))
		args = append(args, label)
	}

	any := CompactNonEmptyStrings(filter.LabelsAny)
	switch {
	case degenerate(filter.LabelsAny, any):
		clauses = append(clauses, "1=0")
	case len(any) > 0:
		ph, lArgs := InPlaceholders(any)
		clauses = append(clauses, fmt.Sprintf("%s IN (SELECT issue_id FROM %s WHERE label IN (%s))", col("id"), tables.Labels, ph))
		args = append(args, lArgs...)
	}

	// An all-empty exclusion set excludes nothing, which cannot widen the set
	// beyond what the other clauses allow — but a degenerate ExcludeLabels ALONE
	// would otherwise leave the sweep global, so it fails closed too.
	excl := CompactNonEmptyStrings(filter.ExcludeLabels)
	switch {
	case degenerate(filter.ExcludeLabels, excl):
		clauses = append(clauses, "1=0")
	case len(excl) > 0:
		ph, lArgs := InPlaceholders(excl)
		clauses = append(clauses, fmt.Sprintf("%s NOT IN (SELECT issue_id FROM %s WHERE label IN (%s))", col("id"), tables.Labels, ph))
		args = append(args, lArgs...)
	}
	return clauses, args
}

// ReclaimScopeSQL is BuildReclaimScopeClauses rendered as a suffix ready to be
// appended to an existing WHERE clause (leading " AND ", or empty when the
// filter constrains nothing).
func ReclaimScopeSQL(filter types.ReclaimFilter, tables FilterTables, alias string) (string, []any) {
	clauses, args := BuildReclaimScopeClauses(filter, tables, alias)
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}
