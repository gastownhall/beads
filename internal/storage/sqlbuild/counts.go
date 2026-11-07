package sqlbuild

import (
	"fmt"
	"strings"
)

// ReadyWorkIssueColumns is IssueSelectColumns qualified with the "i." alias
// used by the counts mega-query. The lease overlay columns keep their own
// leases. qualifier (the mega-query FROM includes LeaseJoin("i")).
var ReadyWorkIssueColumns = QualifyColumns(IssueBaseColumns, "i.") + ", " + LeaseSelectColumns

// ReadyWorkIssueColumnsLite is the lite counterpart of ReadyWorkIssueColumns,
// selected by CountsHydration.Lite. The scan side is
// issueops.ScanIssueLiteFrom, reached through ScanReadyWorkRowWithCounts.
var ReadyWorkIssueColumnsLite = QualifyColumns(IssueBaseColumnsLite, "i.") + ", " + LeaseSelectColumns

// DepJSONObject renders one dependency row as JSON for JSON_ARRAYAGG
// aggregation in the counts mega-query. Field names must match the JSON tags
// of types.Dependency.
const DepJSONObject = `JSON_OBJECT(
	'issue_id', issue_id,
	'depends_on_id', COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external),
	'type', type,
	'created_at', DATE_FORMAT(created_at, '%Y-%m-%dT%H:%i:%sZ'),
	'created_by', created_by,
	'metadata', CAST(metadata AS CHAR),
	'thread_id', thread_id
)`

// CountsHydration selects what the counts mega-query materializes per row.
// Every member is an opt-OUT: the zero value hydrates everything.
//
// None of them changes WHICH ROWS come back or in what order. The aggregates
// hang off LEFT JOINs that preserve every driver row, so dropping one drops a
// column's value and nothing else; Lite changes only the driver's own SELECT
// list, and no filter predicate is expressed in the SELECT.
//
// SkipLabels leaves labels_json NULL, which the scan turns into no labels.
// SkipCounts leaves the three cardinalities 0, which a caller must read as
// unknown rather than as none — the promise issueops.ListRequest.SkipCounts
// makes above storage. Both keep the projection's SHAPE: the scan side reads
// the six extra columns positionally, so a suppressed aggregate is projected
// as a constant in place rather than removed.
//
// LITE IS THE ONE THAT CHANGES THE SHAPE, which is why it is not merely a
// third constant substitution. It swaps ReadyWorkIssueColumns for
// ReadyWorkIssueColumnsLite, so the driver projects six fewer columns and the
// scan must read the matching dest list — ScanReadyWorkRowWithCounts takes the
// whole struct for that reason and dispatches on this field. Substituting
// empty strings in place would have kept one scan path, and was rejected: the
// row would come back with blank free-form text and IsLitePartial=false, which
// is indistinguishable from a genuinely textless row (the ambiguity ga-clgh
// and CommentsOmitted already record). ScanIssueLiteFrom sets the flag.
type CountsHydration struct {
	SkipLabels bool
	SkipCounts bool
	Lite       bool
}

// IssueColumns returns the driver's SELECT list for this hydration.
func (h CountsHydration) IssueColumns() string {
	if h.Lite {
		return ReadyWorkIssueColumnsLite
	}
	return ReadyWorkIssueColumns
}

// SearchCountsSQL renders the counts mega-query: issue rows aliased "i" plus
// labels JSON, dep/rdep/comment counts, parent ID, and dependency JSON, for one
// table family. The scan side is issueops.ScanReadyWorkRowWithCounts, which
// scans hyd.IssueColumns() positionally followed by the six extra columns in
// the order projected here — so the SAME hydration value has to reach both, and
// is the reason the scan takes the struct rather than being told the shape
// separately.
//
// There are two forms of the same projection, selected by ids:
//
//   - Predicate form (ids empty): whereSQL bounds the driver in an inner
//     subquery BEFORE the aggregate LEFT JOINs (see below); orderBySQL and
//     limitSQL stay at the outer level, after the joins. Each count subquery
//     aggregates its whole side table. The caller supplies its own args; the
//     returned args slice is nil.
//
//   - By-IDs form (ids non-empty): whereSQL/orderBySQL/limitSQL are ignored and
//     the driver AND every count subquery are constrained to ids. This keeps
//     each subquery from scanning its whole side table and, crucially, keeps
//     the reverse-blocker rc self-join — whose COALESCE(...) key the pure-Go
//     GMS analyzer cannot auto-index — from re-scanning its full materialization
//     once per driver row. The returned args are the ids repeated once per
//     injection point, in left-to-right placeholder order.
//
// The by-IDs form is result-identical to the predicate form over the same set
// of surviving rows: each per-issue count is a function of the whole dependency
// graph restricted to that issue, so filtering a subquery's input to ids cannot
// change any surviving row's count. dep/comment/parent counts are
// order-insensitive; labels are re-sorted in Go (ScanReadyWorkRowWithCounts).
// deps_json's element order is whatever JSON_ARRAYAGG emits (SQL does not
// guarantee one), but it is the same in both forms: restricting the aggregate's
// input to ids drops only rows for other issues, so the per-issue rows it
// aggregates — and their relative order — are unchanged.
//
// In the predicate form, whereSQL filters the main table in an inner subquery,
// BEFORE the aggregate LEFT JOINs. The joins all preserve every main row, so
// filtering before vs. after them yields the identical row set — but it changes
// the plan Dolt picks: instead of materializing the dep/rdep/comment/label/parent
// aggregates and joining them to every main row before the WHERE prunes the
// result, the joins now drive off the already-narrowed set. That is the win for
// the narrow ephemeral-work searches that dominate the hot path. Only the WHERE
// moves inward; orderBySQL and limitSQL remain after the joins, because ORDER BY
// and LIMIT must see the projected aggregate columns and must apply to the final
// joined result. An empty whereSQL keeps the plain "FROM <main> i" driver: there
// is no predicate to push down, so the derived table would only interpose a
// needless wrapper between the planner and the base table.
//
// That shape REQUIRES that whereSQL reference only main-table columns (or
// correlated subqueries against labels/deps/comments keyed by id) — never the
// six aggregate aliases (labels_json, dep_count, rdep_count, comment_count,
// parent_id, deps_json), which are projected by the OUTER query and are not in
// scope inside the derived subquery. The only producers of whereSQL,
// BuildIssueFilterClauses and BuildReadyWorkWhere, uphold this by construction.
// The invariant is self-enforcing: a predicate that referenced an aggregate
// would reference an out-of-scope column, so Dolt errors at query time rather
// than silently returning wrong rows — the failure is loud, not subtle. (The
// subquery scoping that makes it loud is pinned by TestSearchCountsSQLShape.)
//
// The by-IDs form keeps the plain "FROM <main> i" driver with its id predicate
// at the outer level: its subqueries are already id-constrained, so there is no
// join-then-filter blowup to avoid and no reason to interpose a derived table.
//
// The reverse-blocker rc subquery unions wisp_dependencies only when the caller
// has probed that the table exists (includeWispReverseDeps).
//
// hyd selects which aggregates are computed at all; see CountsHydration.
func SearchCountsSQL(tables FilterTables, ids []string, whereSQL, orderBySQL, limitSQL string, includeWispReverseDeps bool, hyd CountsHydration) (string, []any) {
	return SearchCountsSQLScoped(tables, ids, nil, whereSQL, orderBySQL, limitSQL, includeWispReverseDeps, hyd)
}

// MaxCountsScope bounds a predicate-form scope. A scoped query binds the
// scope at most twelve times, so a larger scope runs unscoped rather than
// approach per-statement placeholder limits.
const MaxCountsScope = 1000

// SearchCountsSQLScoped bounds predicate-form aggregates to explicit filter
// IDs. The driver also requires i.id IN (scope), preserving each surviving
// row's aggregates. Callers must supply a scope containing every ID their
// predicate can match, as BuildIssueFilterClauses does for IssueFilter.IDs.
// Returned arguments follow the caller's predicate arguments. An empty or
// oversized scope leaves the predicate form unchanged; by-IDs ignores scope.
func SearchCountsSQLScoped(tables FilterTables, ids, scope []string, whereSQL, orderBySQL, limitSQL string, includeWispReverseDeps bool, hyd CountsHydration) (string, []any) {
	byIDs := len(ids) > 0
	whereBody, whereOK := strings.CutPrefix(whereSQL, "WHERE ")
	if !byIDs && ((whereSQL != "" && !whereOK) || len(scope) > MaxCountsScope) {
		scope = nil // an unrecognized predicate shape or an oversized scope is never scoped
	}
	scoped := byIDs || len(scope) > 0
	if !byIDs {
		ids = scope
	}
	inSQL, idArgs := InPlaceholders(ids)

	// Per-subquery id constraints. Empty strings in the unscoped predicate form
	// leave the projection unchanged; with ids or a scope they push the id set
	// down so no subquery aggregates more than that set's worth of rows.
	var labelWhere, depBlocksExtra, ccWhere, pcExtra, depWhere string
	if scoped {
		labelWhere = fmt.Sprintf("WHERE issue_id IN (%s)", inSQL)
		depBlocksExtra = fmt.Sprintf(" AND issue_id IN (%s)", inSQL)
		ccWhere = fmt.Sprintf("WHERE issue_id IN (%s)", inSQL)
		pcExtra = fmt.Sprintf(" AND issue_id IN (%s)", inSQL)
		depWhere = fmt.Sprintf("WHERE issue_id IN (%s)", inSQL)
	}
	// deps_json's element order is the order its rows reach JSON_ARRAYAGG. The
	// unscoped predicate form scans dependencies in primary-key order, while an
	// issue_id-constrained read comes back in (issue_id, target) index order.
	// The first physical column is the id primary key on current schemas and
	// issue_id on older schemas that read-only opens may still encounter.
	// Retain that observed scan order without requiring an id column.
	depFrom := tables.Dependencies
	if scoped && !byIDs {
		depFrom = fmt.Sprintf("(SELECT * FROM %s %s ORDER BY 1) scoped_deps", tables.Dependencies, depWhere)
		depWhere = ""
	}

	// The reverse-blocker rows. Scoped, the target constraint is rendered as
	// per-column branches (DepTargetUnion) that seek the (type, target)
	// indexes; "COALESCE(...) IN (ids)" could use none and scanned every
	// 'blocks' edge once per query.
	var rcArgs []any
	reverseBlockerSelect := fmt.Sprintf(`
				SELECT %s AS dep_id
				FROM dependencies WHERE type = 'blocks'
	`, DepTargetExpr)
	if scoped {
		reverseBlockerSelect = DepTargetUnion("dependencies", "", func(col string) string { return col + " AS dep_id" }, fmt.Sprintf("IN (%s)", inSQL), " AND type = 'blocks'")
		rcArgs = DepTargetUnionArgs(idArgs, nil)
	}
	if includeWispReverseDeps {
		wispSelect := fmt.Sprintf(`SELECT %s AS dep_id
				FROM wisp_dependencies WHERE type = 'blocks'`, DepTargetExpr)
		if scoped {
			wispSelect = DepTargetUnion("wisp_dependencies", "", func(col string) string { return col + " AS dep_id" }, fmt.Sprintf("IN (%s)", inSQL), " AND type = 'blocks'")
			rcArgs = append(rcArgs, DepTargetUnionArgs(idArgs, nil)...)
		}
		reverseBlockerSelect += `
				UNION ALL
				` + wispSelect
	}

	labelsSelect := "l.labels_json AS labels_json"
	labelsJoin := fmt.Sprintf(`
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(label) AS labels_json
			FROM %s
			%s
			GROUP BY issue_id
		) l ON l.issue_id = i.id`, tables.Labels, labelWhere)
	if hyd.SkipLabels {
		labelsSelect = "NULL AS labels_json"
		labelsJoin = ""
	}

	countsSelect := `COALESCE(dc.cnt, 0) AS dep_count,
			COALESCE(rc.cnt, 0) AS rdep_count,
			COALESCE(cc.cnt, 0) AS comment_count`
	countsJoins := fmt.Sprintf(`
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			WHERE type = 'blocks'%s
			GROUP BY issue_id
		) dc ON dc.issue_id = i.id
		LEFT JOIN (
			SELECT dep_id, COUNT(*) AS cnt FROM (
				%s
			) all_blockers GROUP BY dep_id
		) rc ON rc.dep_id = i.id
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			%s
			GROUP BY issue_id
		) cc ON cc.issue_id = i.id`,
		tables.Dependencies, depBlocksExtra,
		reverseBlockerSelect,
		tables.Comments, ccWhere)
	if hyd.SkipCounts {
		// Constants in the same three positions: the scan reads these columns
		// by index, and the rc join above is the one the embedded engine
		// cannot index (see the by-IDs rationale above).
		countsSelect = `0 AS dep_count,
			0 AS rdep_count,
			0 AS comment_count`
		countsJoins = ""
	}

	// Predicate form with a filter: whereSQL filters the main table inside a
	// derived table so the aggregate joins drive off the already-narrowed set;
	// ORDER BY and LIMIT stay outer, after the joins. Everything else — empty
	// whereSQL (nothing to push down) and the by-IDs form (subqueries already
	// id-constrained) — keeps the plain driver.
	// A scoped predicate form also bounds the derived driver by the scope.
	driverSQL := fmt.Sprintf("%s i", tables.Main)
	driverWhere := whereSQL
	if !byIDs && scoped {
		if driverWhere == "" {
			driverWhere = fmt.Sprintf("WHERE i.id IN (%s)", inSQL)
		} else {
			driverWhere = fmt.Sprintf("WHERE (%s) AND i.id IN (%s)", whereBody, inSQL)
		}
	}
	if !byIDs && driverWhere != "" {
		driverSQL = fmt.Sprintf(`(
			SELECT i.*
			FROM %s i
			%s
		) i`, tables.Main, driverWhere)
	}
	outerClause := fmt.Sprintf("%s\n\t\t%s", orderBySQL, limitSQL)
	if byIDs {
		outerClause = fmt.Sprintf("WHERE i.id IN (%s)", inSQL)
	}

	sqlText := fmt.Sprintf(`
		SELECT %s,
			%s,
			%s,
			pc.parent_id     AS parent_id,
			d.deps_json      AS deps_json
		FROM %s
		%s
		%s
		%s
		LEFT JOIN (
			SELECT issue_id,
			       MIN(%s) AS parent_id
			FROM %s
			WHERE type = 'parent-child'%s
			GROUP BY issue_id
		) pc ON pc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(%s) AS deps_json
			FROM %s
			%s
			GROUP BY issue_id
		) d ON d.issue_id = i.id
		%s
	`,
		hyd.IssueColumns(),
		labelsSelect,
		countsSelect,
		driverSQL,
		LeaseJoin("i"),
		labelsJoin,
		countsJoins,
		DepTargetExpr, tables.Dependencies, pcExtra,
		DepJSONObject, depFrom, depWhere,
		outerClause,
	)

	if !scoped {
		return sqlText, nil
	}

	// args follow the placeholder order in sqlText: the scoped predicate
	// form's driver scope (after the caller's whereSQL args), labels join
	// (unless skipped), dc, the rc branches (three per dependency table), cc
	// (all unless the counts are skipped), pc, d, then the by-IDs driver.
	args := make([]any, 0, len(idArgs)*14)
	if !byIDs {
		args = append(args, idArgs...) // scoped driver
	}
	if !hyd.SkipLabels {
		args = append(args, idArgs...)
	}
	if !hyd.SkipCounts {
		args = append(args, idArgs...) // dc
		args = append(args, rcArgs...) // rc branches
		args = append(args, idArgs...) // cc
	}
	args = append(args, idArgs...) // pc
	args = append(args, idArgs...) // d
	if !byIDs {
		return sqlText, args
	}
	args = append(args, idArgs...) // driver
	return sqlText, args
}
