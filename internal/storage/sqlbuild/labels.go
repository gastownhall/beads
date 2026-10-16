package sqlbuild

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// LabelSearchPlan rewrites label predicates (Labels AND-set, LabelsAny) from
// IN-subqueries into JOINs against the labels table, which the optimizer
// handles far better on large corpora. Filter is the input filter with the
// rewritten label fields cleared; callers pass it (not the original) to
// BuildIssueFilterClauses and then merge Where/Args via MergeInto.
type LabelSearchPlan struct {
	FromSQL  string
	Where    []string
	Args     []any
	Distinct bool
	Filter   types.IssueFilter
}

// MergeInto prepends the plan's label clauses to filter-built clauses,
// preserving the historical clause and arg ordering of both stacks.
// Single-use: the result shares the plan's backing arrays, so merging one
// plan into two clause sets could clobber the first result's tail.
func (p LabelSearchPlan) MergeInto(where []string, args []any) ([]string, []any) {
	if len(p.Where) == 0 {
		return where, args
	}
	return append(p.Where, where...), append(p.Args, args...)
}

// BuildLabelDrivenSearch produces the FROM clause (with label JOINs when label
// predicates are present) and the residual filter for a table family.
func BuildLabelDrivenSearch(filter types.IssueFilter, tables FilterTables) LabelSearchPlan {
	labels := CompactNonEmptyStrings(filter.Labels)
	labelsAny := CompactNonEmptyStrings(filter.LabelsAny)
	if len(labels) == 0 && len(labelsAny) == 0 {
		return LabelSearchPlan{FromSQL: tables.Main, Filter: filter}
	}

	filterForClauses := filter
	filterForClauses.Labels = nil
	filterForClauses.LabelsAny = nil

	var joins, where []string
	var args []any

	for i, label := range labels {
		alias := fmt.Sprintf("label_filter_%d", i)
		joins = append(joins, fmt.Sprintf("JOIN %s %s ON %s.issue_id = %s.id", tables.Labels, alias, alias, tables.Main))
		where = append(where, fmt.Sprintf("%s.label = ?", alias))
		args = append(args, label)
	}

	if len(labelsAny) > 0 {
		alias := "label_filter_any"
		joins = append(joins, fmt.Sprintf("JOIN %s %s ON %s.issue_id = %s.id", tables.Labels, alias, alias, tables.Main))
		placeholders := make([]string, len(labelsAny))
		for i, label := range labelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		where = append(where, fmt.Sprintf("%s.label IN (%s)", alias, strings.Join(placeholders, ", ")))
	}

	return LabelSearchPlan{
		FromSQL:  tables.Main + " " + strings.Join(joins, " "),
		Where:    where,
		Args:     args,
		Distinct: true,
		Filter:   filterForClauses,
	}
}

// LabelSetClauses builds the AND / OR / NOT label predicates as IN-subqueries
// against tables.Labels, keyed on idExpr. Semantics are the ones users already
// know from bd list and bd ready: labels is AND (carry all of them), labelsAny
// is OR (carry at least one), exclude drops an issue carrying any. Clause and
// arg ordering, and the treatment of empty entries, mirror the equivalent
// blocks in BuildIssueFilterClauses so every listing command filters labels
// identically.
//
// BuildLabelDrivenSearch rewrites the AND/OR pair into JOINs for the search
// path, which the optimizer prefers on large corpora. This is the subquery
// form, for callers whose FROM clause is fixed and cannot take a join.
func LabelSetClauses(idExpr string, tables FilterTables, labels, labelsAny, exclude []string) ([]string, []any) {
	var where []string
	var args []any

	for _, label := range labels {
		where = append(where, fmt.Sprintf("%s IN (SELECT issue_id FROM %s WHERE label = ?)", idExpr, tables.Labels))
		args = append(args, label)
	}
	if len(labelsAny) > 0 {
		placeholders := make([]string, len(labelsAny))
		for i, label := range labelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		where = append(where, fmt.Sprintf("%s IN (SELECT issue_id FROM %s WHERE label IN (%s))",
			idExpr, tables.Labels, strings.Join(placeholders, ", ")))
	}
	if len(exclude) > 0 {
		placeholders := make([]string, len(exclude))
		for i, label := range exclude {
			placeholders[i] = "?"
			args = append(args, label)
		}
		where = append(where, fmt.Sprintf("%s NOT IN (SELECT issue_id FROM %s WHERE label IN (%s))",
			idExpr, tables.Labels, strings.Join(placeholders, ", ")))
	}

	return where, args
}

// RequiresClause is the execution-context predicate behind `--ctx`: hide
// every issue carrying a label under types.RequiresLabelPrefix whose token the
// declared context does not satisfy. It is the one clause the other label
// filters cannot build (a NOT IN under a prefix needs the value universe for
// ExcludeLabels and a negative match LabelRegex cannot write), so ready, list
// and blocked all call this rather than spelling it three times.
//
// A nil ctx is "not declared" and yields no clause (ok=false): the row set is
// exactly what it was before the predicate existed. A non-nil ctx, EVEN EMPTY,
// yields the clause; with no satisfied labels it hides every requiring issue,
// which is the strict reading a declared context asks for. The satisfied set
// comes from types.RequiresSatisfiedLabels so the satisfaction rule has one
// definition. The LIKE pattern escapes the prefix the way the glob filter
// does (globToLikePattern's '|' escape), so a prefix containing '_' or '%'
// would still match literally.
func RequiresClause(idExpr string, tables FilterTables, ctx []string) (string, []any, bool) {
	if ctx == nil {
		return "", nil, false
	}
	like := strings.NewReplacer("|", "||", "%", "|%", "_", "|_").Replace(types.RequiresLabelPrefix) + "%"
	satisfied := types.RequiresSatisfiedLabels(ctx)
	if len(satisfied) == 0 {
		return fmt.Sprintf("%s NOT IN (SELECT issue_id FROM %s WHERE label LIKE ? ESCAPE '|')", idExpr, tables.Labels),
			[]any{like}, true
	}
	placeholders := make([]string, len(satisfied))
	args := make([]any, 0, len(satisfied)+1)
	args = append(args, like)
	for i, label := range satisfied {
		placeholders[i] = "?"
		args = append(args, label)
	}
	return fmt.Sprintf("%s NOT IN (SELECT issue_id FROM %s WHERE label LIKE ? ESCAPE '|' AND label NOT IN (%s))",
		idExpr, tables.Labels, strings.Join(placeholders, ", ")), args, true
}
