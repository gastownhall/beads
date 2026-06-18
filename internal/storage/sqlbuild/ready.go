package sqlbuild

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// ReadyWorkExcludeTypes returns the issue types excluded from ready work by
// default, plus any caller extras (deduped, empty entries dropped). The infra
// types come from domain.DefaultInfraTypes so that adding an infra type
// changes both stacks together.
func ReadyWorkExcludeTypes(extra []types.IssueType) []types.IssueType {
	out := []types.IssueType{
		types.IssueType("merge-request"),
		types.TypeGate,
		types.TypeMolecule,
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
		return ReadyWorkOrder{SQL: fmt.Sprintf("ORDER BY %s ASC, %s DESC, id ASC", priorityCol, createdCol)}
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
		return ReadyWorkOrder{SQL: fmt.Sprintf("ORDER BY %s ASC, %s DESC, id ASC", priorityCol, createdCol)}
	}
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
}

// BuildReadyWorkWhere renders the full ready-work WHERE clause for one table
// family. Both stacks must keep ready semantics identical (Seam A parity
// suite); all ready predicates live here.
func BuildReadyWorkWhere(filter types.WorkFilter, tables FilterTables, in ReadyWorkWhereInputs) (string, []any, error) {
	var statusClause string
	if filter.Status != "" {
		statusClause = "status = ?"
	} else {
		statusClause = "status IN ('open', 'in_progress')"
	}
	whereClauses := []string{
		statusClause,
		"(pinned = 0 OR pinned IS NULL)",
		"is_blocked = 0",
	}
	if !filter.IncludeEphemeral {
		whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
	}
	var args []any
	if filter.Status != "" {
		args = append(args, string(filter.Status))
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
		for start := 0; start < len(in.DeferredChildIDs); start += QueryBatchSize {
			end := start + QueryBatchSize
			if end > len(in.DeferredChildIDs) {
				end = len(in.DeferredChildIDs)
			}
			placeholders, batchArgs := InPlaceholders(in.DeferredChildIDs[start:end])
			args = append(args, batchArgs...)
			whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (%s)", placeholders))
		}
	}

	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label = ?)", tables.Labels))
			args = append(args, label)
		}
	}
	if len(filter.ExcludeLabels) > 0 {
		placeholders := make([]string, len(filter.ExcludeLabels))
		for i, label := range filter.ExcludeLabels {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE label IN (%s))", tables.Labels, strings.Join(placeholders, ", ")))
	}

	// Parent filtering: return all transitive descendants of parentID.
	// GH#3396: a one-hop subquery silently dropped grandchildren despite the
	// help text and WorkFilter.ParentID godoc both promising recursion.
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		parentClauses := []string{fmt.Sprintf("(id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child'))", tables.Dependencies)}
		args = append(args, parentID)
		for start := 0; start < len(in.ParentDescendantIDs); start += QueryBatchSize {
			end := start + QueryBatchSize
			if end > len(in.ParentDescendantIDs) {
				end = len(in.ParentDescendantIDs)
			}
			placeholders, batchArgs := InPlaceholders(in.ParentDescendantIDs[start:end])
			parentClauses = append(parentClauses, fmt.Sprintf("id IN (%s)", placeholders))
			args = append(args, batchArgs...)
		}
		whereClauses = append(whereClauses, "("+strings.Join(parentClauses, " OR ")+")")
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
