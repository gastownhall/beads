package sqlbuild

import "fmt"

// ReadyWorkIssueColumns is IssueSelectColumns qualified with the "i." alias
// used by the counts mega-query. The lease overlay columns keep their own
// leases. qualifier (the mega-query FROM includes LeaseJoin("i")).
var ReadyWorkIssueColumns = QualifyColumns(IssueBaseColumns, "i.") + ", " + LeaseSelectColumns

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

// CountsHydration selects which per-row aggregates the counts mega-query
// computes. Both members are opt-OUTs: the zero value hydrates everything.
//
// Neither one changes WHICH ROWS come back or in what order — the aggregates
// hang off LEFT JOINs that preserve every driver row, so dropping one drops a
// column's value and nothing else. Each also keeps the projection's SHAPE:
// the scan side (issueops.ScanReadyWorkRowWithCounts) reads the six extra
// columns positionally, so a suppressed aggregate is projected as a constant
// in place rather than removed.
//
// SkipLabels leaves labels_json NULL, which the scan turns into no labels.
// SkipCounts leaves the three cardinalities 0, which a caller must read as
// unknown rather than as none — the promise issueops.ListRequest.SkipCounts
// makes above storage.
type CountsHydration struct {
	SkipLabels bool
	SkipCounts bool
}

// SearchCountsSQL renders the counts mega-query: full issue rows aliased "i"
// plus labels JSON, dep/rdep/comment counts, parent ID, and dependency JSON,
// for one table family. The scan side is issueops.ScanReadyWorkRowWithCounts,
// which scans IssueSelectColumns positionally followed by the six extra
// columns in the order projected here.
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
	byIDs := len(ids) > 0
	inSQL, idArgs := InPlaceholders(ids)

	// Per-subquery id constraints. Empty strings in the predicate form leave the
	// projection unchanged; in the by-IDs form they push the page down so no
	// subquery aggregates more than the page's worth of rows.
	var labelWhere, depBlocksExtra, rcDepExtra, rcWispExtra, ccWhere, pcExtra, depWhere string
	if byIDs {
		labelWhere = fmt.Sprintf("WHERE issue_id IN (%s)", inSQL)
		depBlocksExtra = fmt.Sprintf(" AND issue_id IN (%s)", inSQL)
		rcDepExtra = fmt.Sprintf(" AND %s IN (%s)", DepTargetExpr, inSQL)
		rcWispExtra = fmt.Sprintf(" AND %s IN (%s)", DepTargetExpr, inSQL)
		ccWhere = fmt.Sprintf("WHERE issue_id IN (%s)", inSQL)
		pcExtra = fmt.Sprintf(" AND issue_id IN (%s)", inSQL)
		depWhere = fmt.Sprintf("WHERE issue_id IN (%s)", inSQL)
	}

	reverseBlockerSelect := fmt.Sprintf(`
				SELECT %s AS dep_id
				FROM dependencies WHERE type = 'blocks'%s
	`, DepTargetExpr, rcDepExtra)
	if includeWispReverseDeps {
		reverseBlockerSelect += fmt.Sprintf(`
				UNION ALL
				SELECT %s AS dep_id
				FROM wisp_dependencies WHERE type = 'blocks'%s
		`, DepTargetExpr, rcWispExtra)
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
	driverSQL := fmt.Sprintf("%s i", tables.Main)
	if !byIDs && whereSQL != "" {
		driverSQL = fmt.Sprintf(`(
			SELECT i.*
			FROM %s i
			%s
		) i`, tables.Main, whereSQL)
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
		ReadyWorkIssueColumns,
		labelsSelect,
		countsSelect,
		driverSQL,
		LeaseJoin("i"),
		labelsJoin,
		countsJoins,
		DepTargetExpr, tables.Dependencies, pcExtra,
		DepJSONObject, tables.Dependencies, depWhere,
		outerClause,
	)

	if !byIDs {
		return sqlText, nil
	}

	// args follow the placeholder order in sqlText: labels join (unless
	// skipped), dc, rc dependencies branch, rc wisp branch (if any), cc (all
	// four unless the counts are skipped), pc, d, then the driver.
	args := make([]any, 0, len(idArgs)*8)
	if !hyd.SkipLabels {
		args = append(args, idArgs...)
	}
	if !hyd.SkipCounts {
		args = append(args, idArgs...) // dc
		args = append(args, idArgs...) // rc dependencies
		if includeWispReverseDeps {
			args = append(args, idArgs...) // rc wisp_dependencies
		}
		args = append(args, idArgs...) // cc
	}
	args = append(args, idArgs...) // pc
	args = append(args, idArgs...) // d
	args = append(args, idArgs...) // driver
	return sqlText, args
}
