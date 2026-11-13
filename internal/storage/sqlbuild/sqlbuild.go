// Package sqlbuild holds the pure SQL-text builders shared by the classic
// issueops stack (production, *sql.Tx) and the domain/db repository stack
// (proxied-server, Runner). Functions here map (filter, tables) -> (sql, args)
// and are execution-context-free: no database handles, no transactions.
//
// Both stacks must produce identical row sets for identical filters (pinned
// by the Seam A parity suite in internal/storage/domain/db). Keeping the
// query text in one place is what prevents the two implementations from
// drifting; see bd-6dnrw.46 for the unify-vs-duplicate decision. Execution
// orchestration (issues+wisps merge strategy, hydration, probes) is
// intentionally NOT unified and stays in each stack.
package sqlbuild

import (
	"fmt"
	"strings"
)

// FilterTables configures table names for the filter builders, allowing the
// same filter logic to target both the issues and wisps table families.
type FilterTables struct {
	Main         string // "issues" or "wisps"
	Labels       string // "labels" or "wisp_labels"
	Dependencies string // "dependencies" or "wisp_dependencies"
	Comments     string // "comments" or "wisp_comments"
}

var (
	IssuesFilterTables = FilterTables{Main: "issues", Labels: "labels", Dependencies: "dependencies", Comments: "comments"}
	WispsFilterTables  = FilterTables{Main: "wisps", Labels: "wisp_labels", Dependencies: "wisp_dependencies", Comments: "wisp_comments"}
)

// OptionalWispTable reports whether name is a wisp-plane table a database may
// legitimately not have, which is the set a wisp query may treat as "no wisps"
// rather than a failure. Both stacks read it so the two cannot disagree about
// what a missing table means.
func OptionalWispTable(name string) bool {
	return strings.EqualFold(name, "wisps") || strings.EqualFold(name, "wisp_dependencies")
}

// DepTargetExpr resolves a dependency row's target across the three
// mutually-exclusive target columns.
const DepTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

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

// IssueBaseColumns is the column list for the issues/wisps row itself,
// without the lease overlay. Use IssueSelectColumns for full hydration;
// this split exists so callers that alias the main table (QualifyColumns)
// can qualify the row columns without mangling the leases.* references.
const IssueBaseColumns = `id, content_hash, title, description, design, acceptance_criteria, notes,
	       status, priority, issue_type, assignee, estimated_minutes,
	       created_at, created_by, owner, updated_at, started_at, closed_at, external_ref, spec_id,
	       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason, closed_by_session,
	       sender, ephemeral, no_history, wisp_type, pinned, is_template,
	       await_type, await_id, timeout_ns, waiters,
	       mol_type,
	       event_kind, actor, target, payload,
	       due_at, defer_until,
	       work_type, source_system, metadata, row_lock, storage_class, arrival_seq`

// IssueBaseColumnsLite is IssueBaseColumns minus the heavy TEXT columns
// (issueops.HeavyDropList). It lives here rather than beside its scan function
// for the same reason IssueBaseColumns does: the counts mega-query aliases the
// main table and has to qualify the row columns without mangling the leases.*
// references, and that query is rendered in this package. issueops re-exports
// the full list as IssueSelectColumnsLite and pins it against IssueBaseColumns
// with the schema-parity test.
const IssueBaseColumnsLite = `id, content_hash, title,
	       status, priority, issue_type, assignee, estimated_minutes,
	       created_at, created_by, owner, updated_at, started_at, closed_at, external_ref, spec_id,
	       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason, closed_by_session,
	       sender, ephemeral, no_history, wisp_type, pinned, is_template,
	       await_type, await_id, timeout_ns,
	       mol_type,
	       event_kind, actor, target,
	       due_at, defer_until,
	       work_type, source_system, metadata, row_lock, storage_class, arrival_seq`

// LeaseSelectColumns is the lease overlay for full issue hydration. Leases
// live in the ephemeral leases table (bd-lrgn1), not on the issues row, so
// every query selecting these must also add LeaseJoin to its FROM clause —
// a query that forgets the join fails loudly on the leases.* reference.
const LeaseSelectColumns = `leases.lease_expires_at, leases.heartbeat_at, leases.granted_node`

// IssueSelectColumns is the canonical column list for full issue hydration.
// Every query that reads a complete types.Issue should use this constant
// and include LeaseJoin(table) in its FROM clause; the scan side is
// issueops.ScanIssueFrom, which scans positionally and must stay in
// column-for-column agreement with this list.
const IssueSelectColumns = IssueBaseColumns + `,
	       ` + LeaseSelectColumns

// IssueSelectColumnsLite is the lite counterpart of IssueSelectColumns. The
// scan side is issueops.ScanIssueLiteFrom and the same LeaseJoin requirement
// applies.
const IssueSelectColumnsLite = IssueBaseColumnsLite + `,
	       ` + LeaseSelectColumns

// LeaseJoin returns the FROM-clause fragment that overlays the ephemeral
// leases table onto the given issues/wisps table reference (a table name or
// alias). LEFT JOIN: rows without a live claim have no lease row and hydrate
// nil lease fields.
func LeaseJoin(tableRef string) string {
	return "LEFT JOIN leases ON leases.issue_id = " + tableRef + ".id"
}

// QueryBatchSize bounds IN-clause sizes when long ID lists are folded into
// WHERE fragments.
const QueryBatchSize = 200

// QualifyColumns prefixes every column in a comma-separated list (e.g.
// IssueSelectColumns) with the given table qualifier such as "i.".
func QualifyColumns(columns, prefix string) string {
	parts := ColumnNames(columns)
	for i, p := range parts {
		parts[i] = prefix + p
	}
	return strings.Join(parts, ", ")
}

// ColumnNames splits a comma-separated column list such as IssueBaseColumns
// into its entries, trimmed of the newlines and indentation the constants are
// laid out with.
func ColumnNames(columns string) []string {
	raw := strings.ReplaceAll(columns, "\n", " ")
	raw = strings.ReplaceAll(raw, "\t", " ")
	parts := strings.Split(raw, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// InPlaceholders renders "?,?,..." and the matching arg slice for an IN clause.
func InPlaceholders[T ~string](values []T) (string, []any) {
	ph := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		ph[i] = "?"
		args[i] = string(v)
	}
	return strings.Join(ph, ","), args
}

// CompactNonEmptyStrings drops empty entries, returning nil when none remain.
func CompactNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
