package workapi

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/query"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// DefaultQueryLimit is the default number of rows `bd query` returns when the
// caller does not ask for a specific limit. Both surfaces register their limit
// knob from this constant so they cannot drift apart; 0 still means unlimited.
const DefaultQueryLimit = 50

// QueryPlan is an evaluated query request: what to ask the database, what to
// decide in Go afterwards, and how to cut the page.
//
// The two executions of one query are NOT the same shape. A filter-expressible
// expression is a filter the database answers whole; an expression with an OR
// or a general NOT is a base filter plus a Go predicate no seam can push down.
// Both implementations of issueops.Querier build one of these, so which shape
// an expression takes — and what row limit it may carry — is decided once here.
type QueryPlan struct {
	// Filter is what the database is asked. For a predicate query it carries
	// the expression's base filters and NO row limit; see BuildQueryPlan.
	Filter types.IssueFilter
	// Predicate is the in-memory half of the expression, or nil when Filter
	// alone answers it exactly.
	Predicate func(*types.Issue) bool
	// Limit is the number of rows the CALLER receives, which is not always
	// what Filter carries. 0 means unlimited.
	Limit int
	// Offset is the number of matching rows to skip. It is reported here rather
	// than written onto Filter because only the filter-expressible shape may
	// push it down: skipping candidate rows before a predicate runs would drop
	// rows the predicate never accepted (issueops/querier.go:70-86).
	Offset int
	// SortBy and Reverse are the display order, applied by the page epilogue.
	SortBy  string
	Reverse bool
}

// RequiresPredicate reports whether this plan is answered in two steps.
func (p QueryPlan) RequiresPredicate() bool { return p.Predicate != nil }

// BuildQueryPlan turns a query request into its executable plan: the parse, the
// evaluation, the default closed exclusion, the limit defaulting, and the row
// bound each shape of query may carry.
//
// THE ROW BOUND IS THE WHOLE REASON THIS IS ONE FUNCTION. A predicate query's
// Filter gets NO limit — the predicate must see every candidate row, or the
// page it produces is an arbitrary prefix of the answer reported as the whole
// of it (issueops/querier.go:118-133). The front doors used to bound that query
// at max(3*Limit, 100) and filter what came back; that bound is gone.
//
// It reads no configuration and touches no store, so both implementations share
// it without supplying a config source.
func BuildQueryPlan(in issueops.QueryRequest) (QueryPlan, error) {
	expression := strings.TrimSpace(in.Expression)
	if expression == "" {
		return QueryPlan{}, invalidQueryExpression("an expression is required")
	}
	limit := LimitOr(in.Limit, DefaultQueryLimit)
	if limit < 0 {
		return QueryPlan{}, fmt.Errorf("invalid limit %d: a query limit must be zero or greater; 0 means unlimited%.0w",
			limit, issueops.ErrValidation)
	}
	if in.Offset < 0 {
		return QueryPlan{}, fmt.Errorf("invalid offset %d: a query offset must be zero or greater%.0w",
			in.Offset, issueops.ErrValidation)
	}
	if in.Offset > 0 && in.SortBy != "" {
		return QueryPlan{}, fmt.Errorf(
			"invalid offset: an offset cannot be combined with a display order, because the order is applied to the rows the query bounded and each page would be sorted for itself%.0w",
			issueops.ErrValidation)
	}

	node, err := query.Parse(expression)
	if err != nil {
		return QueryPlan{}, invalidQueryExpression(err.Error())
	}
	result, err := query.NewEvaluator(time.Now()).Evaluate(node)
	if err != nil {
		return QueryPlan{}, invalidQueryExpression(err.Error())
	}

	plan := QueryPlan{
		Filter:  result.Filter,
		Limit:   limit,
		Offset:  in.Offset,
		SortBy:  in.SortBy,
		Reverse: in.Reverse,
	}
	if result.RequiresPredicate {
		plan.Predicate = result.Predicate
	} else {
		// The database answers this one exactly, so it may carry the page —
		// and it must carry the ORDER too, for the same reason BuildListFilter
		// pushes SortBy down.
		//
		// Without it the database bounds the page in STORAGE order and the
		// display sort runs afterwards in Go, so which N rows are in the page
		// is whatever order the engine happened to return. The store body
		// over-fetches one row as a has-more probe and sorted that probe INTO
		// the page; the unit-of-work body trims to N natively and never sees
		// it. Two bodies, two different pages, for one `--sort ... --limit N`.
		//
		// Pushed down, the bound and the order are one decision the engine
		// makes: the page is the first N in the requested order on every
		// backend, and the probe row is genuinely last.
		plan.Filter.Limit = limit
		plan.Filter.SortBy = in.SortBy
		plan.Filter.SortDesc = in.Reverse
	}

	// The default closed exclusion, applied only to an expression that has no
	// opinion of its own about status (issueops/querier.go:29-41).
	if !in.IncludeClosed && plan.Filter.Status == nil && !mentionsStatus(node) {
		plan.Filter.ExcludeStatus = append(plan.Filter.ExcludeStatus, types.StatusClosed)
	}
	return plan, nil
}

// invalidQueryExpression is the one refusal shape a bad expression takes. The
// prefix is load-bearing: internal/httpapi maps it back to the `q` parameter
// so an unparseable expression is the documented 400 rather than a 500.
func invalidQueryExpression(detail string) error {
	return fmt.Errorf("invalid query expression: %s%.0w", detail, issueops.ErrValidation)
}

// mentionsStatus reports whether the expression compares `status` anywhere.
// An expression that does keeps its own answer about closed rows; one that
// does not gets the default exclusion.
func mentionsStatus(node query.Node) bool {
	switch n := node.(type) {
	case *query.ComparisonNode:
		return n.Field == "status"
	case *query.AndNode:
		return mentionsStatus(n.Left) || mentionsStatus(n.Right)
	case *query.OrNode:
		return mentionsStatus(n.Left) || mentionsStatus(n.Right)
	case *query.NotNode:
		return mentionsStatus(n.Operand)
	default:
		return false
	}
}

// ApplyQueryPredicate keeps the rows the predicate accepts, in the order they
// arrived. Both implementations of issueops.Querier call it, so "what the
// predicate is applied to" cannot come to mean two things: every row the
// database returned, and the database was not asked for a bounded set.
//
// A nil row, or a row carrying no issue, is dropped rather than offered to the
// predicate.
func ApplyQueryPredicate(rows []*types.IssueWithCounts, predicate func(*types.Issue) bool) []*types.IssueWithCounts {
	if predicate == nil {
		return rows
	}
	out := make([]*types.IssueWithCounts, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.Issue == nil {
			continue
		}
		if predicate(row.Issue) {
			out = append(out, row)
		}
	}
	return out
}

// SkipRows drops the first n rows, and answers with an empty slice rather than
// nil when n consumes them all. It is how an Offset is honored for a predicate
// query, where there is no SQL OFFSET to push it into: the skip runs AFTER the
// predicate, so it skips matches rather than candidates.
func SkipRows(rows []*types.IssueWithCounts, n int) []*types.IssueWithCounts {
	if n <= 0 {
		return rows
	}
	if n >= len(rows) {
		return []*types.IssueWithCounts{}
	}
	return rows[n:]
}
