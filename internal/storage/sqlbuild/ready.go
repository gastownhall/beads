package sqlbuild

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// ReadyWorkExcludeTypes returns the issue types excluded from ready work by
// default, plus any caller extras (deduped, empty entries dropped). Infra types
// stay hidden from ready work, and rig identity beads are also hidden even
// though they are durable issues rather than infra wisps.
func ReadyWorkExcludeTypes(extra []types.IssueType) []types.IssueType {
	out := []types.IssueType{
		types.IssueType("merge-request"),
		types.TypeGate,
		types.TypeMolecule,
		types.IssueType("rig"),
	}
	for _, t := range domain.DefaultInfraTypes() {
		out = append(out, types.IssueType(t))
	}
	seen := make(map[types.IssueType]bool, len(out)+len(extra))
	for _, t := range out {
		seen[t] = true
	}
	for _, t := range extra {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ReadyWorkOrder is an ORDER BY fragment plus any args its CASE expressions
// need (the hybrid policy parameterizes a recency cutoff).
type ReadyWorkOrder struct {
	SQL  string
	Args []any
}

// BuildReadyWorkOrder renders the ready-work ORDER BY for a sort policy.
// createdCol/priorityCol name the sortable columns: real columns
// ("created_at"/"priority") for per-table queries, or the sort_* aliases
// ("sort_created"/"sort_priority") for UNION outer queries.
func BuildReadyWorkOrder(policy types.SortPolicy, createdCol, priorityCol string) ReadyWorkOrder {
	switch policy {
	case types.SortPolicyOldest:
		return ReadyWorkOrder{SQL: fmt.Sprintf("ORDER BY %s ASC, id ASC", createdCol)}
	case types.SortPolicyPriority:
		return ReadyWorkOrder{SQL: fmt.Sprintf("ORDER BY %s ASC, %s ASC, id ASC", priorityCol, createdCol)}
	case types.SortPolicyHybrid, "":
		recentCutoff := time.Now().UTC().Add(-48 * time.Hour)
		return ReadyWorkOrder{
			SQL: fmt.Sprintf(`ORDER BY
			CASE WHEN %s >= ? THEN 0 ELSE 1 END ASC,
			CASE WHEN %s >= ? THEN %s ELSE 999 END ASC,
			%s ASC, id ASC`, createdCol, createdCol, priorityCol, createdCol),
			Args: []any{recentCutoff, recentCutoff},
		}
	default:
		return ReadyWorkOrder{SQL: fmt.Sprintf("ORDER BY %s ASC, %s ASC, id ASC", priorityCol, createdCol)}
	}
}

// readyAffectingTypesSQL renders types.ReadyAffectingDependencyTypes as a
// list of SQL literals. The types are built-in constants, rendered as literals
// like the builder's other 'parent-child' clauses.
func readyAffectingTypesSQL() string {
	depTypes := types.ReadyAffectingDependencyTypes()
	quoted := make([]string, len(depTypes))
	for i, d := range depTypes {
		quoted[i] = "'" + string(d) + "'"
	}
	return strings.Join(quoted, ", ")
}

// OwnReadyAffectingEdgeSQL is the correlated test that the row aliased alias
// is the issue_id of a ready-affecting row in its family's own dependency
// table. Only such a row can be marked by the blocked recompute: every leg of
// its should-be-blocked union selects d.issue_id from that table for one of
// these types. A set is_blocked flag on a row that fails the test is stale (an
// outside merge, a failed post-pull recompute) and must not hide the issue
// from ready work (GH#3268). The flag is only read around, never rewritten;
// bd recompute-blocked repairs it.
//
// The alias is explicit on both sides: inside FROM <dependencies> d a bare id
// would bind to the dependency row's own id column.
func OwnReadyAffectingEdgeSQL(tables FilterTables, alias string) string {
	return fmt.Sprintf("EXISTS (SELECT 1 FROM %s d WHERE d.issue_id = %s.id AND d.type IN (%s))",
		tables.Dependencies, alias, readyAffectingTypesSQL())
}

// StaleBlockedIDsSQL selects the ids of one table family whose is_blocked flag
// is stale: set on a row OwnReadyAffectingEdgeSQL rejects. Each stack resolves
// it once per ready read and hands the ids to BuildReadyWorkWhere in
// ReadyWorkWhereInputs.StaleBlockedIDs, so the ready WHERE keeps the bare,
// indexed is_blocked = 0 whenever no flag is stale.
//
// It reads only the flagged rows and probes each one's own dependency rows by
// issue_id, so its cost follows the flagged rows rather than the dependency
// table: dolt plans it as a range over idx_issues_is_blocked (wisps:
// idx_wisps_is_blocked) and a lookup join on the dependency table's issue_id
// keys. It visits every flagged row even when none is stale, the common case:
// on dolt 2.6 with 50k issues and 200k dependencies it took 30-50 ms at 666
// flagged rows and about 430 ms at 15,000. Folding the same test into the
// ready WHERE instead put a scan of every ready-affecting edge behind each
// ready statement and cost the page query 2-4x once any row was flagged.
func StaleBlockedIDsSQL(tables FilterTables) string {
	return fmt.Sprintf("SELECT t.id FROM %s t WHERE t.is_blocked = 1 AND NOT %s",
		tables.Main, OwnReadyAffectingEdgeSQL(tables, "t"))
}

// readyBlockedClause renders the ready-work blocked exclusion: the indexed
// is_blocked projection decides, except for the rows whose flag is stale
// (StaleBlockedIDsSQL), which it lists by id.
func readyBlockedClause(staleIDs []string) (string, []any) {
	if len(staleIDs) == 0 {
		return "is_blocked = 0", nil
	}
	staleClauses, args := idBatchClauses("id IN (%s)", staleIDs)
	return "(is_blocked = 0 OR " + strings.Join(staleClauses, " OR ") + ")", args
}

// idBatchClauses renders ids as one format clause per QueryBatchSize batch
// (format's one verb takes the batch's placeholders), with the batches' args
// in clause order, so no IN list grows past QueryBatchSize. Every batch lands
// in the same statement, so this does not bound the statement's placeholder
// count.
func idBatchClauses(format string, ids []string) ([]string, []any) {
	var clauses []string
	var args []any
	for start := 0; start < len(ids); start += QueryBatchSize {
		end := min(start+QueryBatchSize, len(ids))
		placeholders, batchArgs := InPlaceholders(ids[start:end])
		clauses = append(clauses, fmt.Sprintf(format, placeholders))
		args = append(args, batchArgs...)
	}
	return clauses, args
}

// ReadyWorkWhereInputs carries the precomputed ID sets the ready-work WHERE
// clause folds in. Computing them takes queries, which is execution-context
// work each stack does its own way.
type ReadyWorkWhereInputs struct {
	// DeferredChildIDs are children of future-deferred parents; consulted
	// only when !filter.IncludeDeferred.
	DeferredChildIDs []string
	// ParentDescendantIDs are the transitive descendants of *filter.ParentID;
	// consulted only when filter.ParentID != nil.
	ParentDescendantIDs []string
	// StaleBlockedIDs holds each family's rows whose is_blocked flag is stale
	// (StaleBlockedIDsSQL over that family's tables), keyed by the family's
	// main table; the flag does not hide them. It is keyed rather than shared
	// like the sets above because an id can name a row in both tables and
	// only one of them may be stale.
	StaleBlockedIDs map[string][]string
}

// readyStatusClause renders the ready-work status predicate and its args.
func readyStatusClause(filter types.WorkFilter) (string, []any) {
	switch {
	case filter.Status == types.StatusOpen:
		// Singular StatusOpen is the `bd ready` pin and means the ready-
		// eligible set: built-in open plus category-active custom statuses,
		// matching the ready_issues view since migration 0025 (GH#5831).
		// Any other singular status stays exact.
		return "(status = ? OR status IN (SELECT name FROM custom_statuses WHERE category = 'active'))", []any{string(filter.Status)}
	case filter.Status != "":
		return "status = ?", []any{string(filter.Status)}
	case len(filter.Statuses) > 0:
		ph, statusArgs := InPlaceholders(filter.Statuses)
		return fmt.Sprintf("status IN (%s)", ph), statusArgs
	default:
		return "status IN ('open', 'in_progress')", nil
	}
}

// readyParentClause renders the --parent predicate: every transitive
// descendant of parentID. GH#3396: a one-hop subquery silently dropped
// grandchildren despite the help text and WorkFilter.ParentID godoc both
// promising recursion.
func readyParentClause(parentID string, tables FilterTables, descendantIDs []string) (string, []any) {
	parentClauses := []string{fmt.Sprintf("(id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child'))", tables.Dependencies)}
	args := []any{parentID}
	descendantClauses, descendantArgs := idBatchClauses("id IN (%s)", descendantIDs)
	parentClauses = append(parentClauses, descendantClauses...)
	args = append(args, descendantArgs...)
	return "(" + strings.Join(parentClauses, " OR ") + ")", args
}

// BuildReadyWorkWhere renders the full ready-work WHERE clause for one table
// family. Both stacks must keep ready semantics identical (Seam A parity
// suite); all ready predicates live here.
//
// Invariant: every clause must reference only main-table columns or correlated
// subqueries keyed by id — never the counts mega-query's aggregate aliases
// (labels_json, dep_count, rdep_count, comment_count, parent_id, deps_json).
// SearchCountsSQL renders this WHERE inside a pre-join subquery where those
// aliases are out of scope. See the SearchCountsSQL doc comment for why a
// violation fails loud.
func BuildReadyWorkWhere(filter types.WorkFilter, tables FilterTables, in ReadyWorkWhereInputs) (string, []any, error) {
	statusClause, args := readyStatusClause(filter)
	blockedClause, blockedArgs := readyBlockedClause(in.StaleBlockedIDs[tables.Main])
	args = append(args, blockedArgs...)
	whereClauses := []string{
		statusClause,
		"(pinned = 0 OR pinned IS NULL)",
		blockedClause,
	}
	if !filter.IncludeEphemeral {
		whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}
	if filter.Type != "" {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, filter.Type)
	} else {
		ph, a := InPlaceholders(ReadyWorkExcludeTypes(filter.ExcludeTypes))
		whereClauses = append(whereClauses, fmt.Sprintf("issue_type NOT IN (%s)", ph))
		args = append(args, a...)
	}
	if filter.Unassigned {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	} else if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	if !filter.IncludeDeferred {
		whereClauses = append(whereClauses, "(defer_until IS NULL OR defer_until <= UTC_TIMESTAMP())")
		deferredClauses, deferredArgs := idBatchClauses("id NOT IN (%s)", in.DeferredChildIDs)
		whereClauses = append(whereClauses, deferredClauses...)
		args = append(args, deferredArgs...)
	}
	excludedClauses, excludedArgs := idBatchClauses("id NOT IN (%s)", filter.ExcludeIDs)
	whereClauses = append(whereClauses, excludedClauses...)
	args = append(args, excludedArgs...)

	// LabelsAny is an OR-set: an issue qualifies if it carries AT LEAST ONE of
	// the labels. Previously this clause was absent entirely, so --label-any was
	// silently dropped on the ready/claim path (with or without --parent) — a
	// worker believed it was fenced when it was not. It AND-combines with Labels
	// (the flag help promises "Can combine with --label"). LabelSetClauses
	// renders the three label sets exactly as bd blocked's scan does.
	labelClauses, labelArgs := LabelSetClauses("id", tables, filter.Labels, filter.LabelsAny, filter.ExcludeLabels)
	whereClauses = append(whereClauses, labelClauses...)
	args = append(args, labelArgs...)
	if filter.LabelPattern != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label LIKE ? ESCAPE '|')", tables.Labels))
		args = append(args, globToLikePattern(filter.LabelPattern))
	}
	if filter.LabelRegex != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label REGEXP ?)", tables.Labels))
		args = append(args, filter.LabelRegex)
	}

	if filter.ParentID != nil {
		parentClause, parentArgs := readyParentClause(*filter.ParentID, tables, in.ParentDescendantIDs)
		whereClauses = append(whereClauses, parentClause)
		args = append(args, parentArgs...)
	}

	if filter.MoleculeID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("(id IN (SELECT issue_id FROM %s WHERE type = 'parent-child' AND %s = ?) OR (id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')))", tables.Dependencies, DepTargetExpr, tables.Dependencies))
		args = append(args, filter.MoleculeID, filter.MoleculeID)
	}

	var err error
	whereClauses, args, err = AppendMetadataClauses(whereClauses, args, filter.HasMetadataKey, filter.MetadataFields)
	if err != nil {
		return "", nil, err
	}

	return "WHERE " + strings.Join(whereClauses, " AND "), args, nil
}
