package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

type readyWorkPredicates struct {
	whereSQL string
	args     []any
}

type readyWorkOrder struct {
	sql  string
	args []any
}

func buildReadyWorkOrder(policy types.SortPolicy) readyWorkOrder {
	switch policy {
	case types.SortPolicyOldest:
		return readyWorkOrder{sql: "ORDER BY sort_created ASC, id ASC"}
	case types.SortPolicyPriority:
		return readyWorkOrder{sql: "ORDER BY sort_priority ASC, sort_created DESC, id ASC"}
	case types.SortPolicyHybrid, "":
		recentCutoff := time.Now().UTC().Add(-48 * time.Hour)
		return readyWorkOrder{
			sql: `ORDER BY
			CASE WHEN sort_created >= ? THEN 0 ELSE 1 END ASC,
			CASE WHEN sort_created >= ? THEN sort_priority ELSE 999 END ASC,
			sort_created ASC, id ASC`,
			args: []any{recentCutoff, recentCutoff},
		}
	default:
		return readyWorkOrder{sql: "ORDER BY sort_priority ASC, sort_created DESC, id ASC"}
	}
}

func readyWorkExcludeTypes(extra []types.IssueType) []types.IssueType {
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

func (r *issueSQLRepositoryImpl) buildReadyWorkPredicates(ctx context.Context, filter types.WorkFilter, tables filterTables) (*readyWorkPredicates, error) {
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
		excludeTypes := readyWorkExcludeTypes(filter.ExcludeTypes)
		ph, a := buildInPlaceholders(excludeTypes)
		whereClauses = append(whereClauses, fmt.Sprintf("issue_type NOT IN (%s)", ph))
		args = append(args, a...)
	}
	if filter.Unassigned {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	} else if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	var deferredChildIDs []string
	if !filter.IncludeDeferred {
		whereClauses = append(whereClauses, "(defer_until IS NULL OR defer_until <= UTC_TIMESTAMP())")
		var dcErr error
		deferredChildIDs, dcErr = r.getChildrenOfDeferredParents(ctx)
		if dcErr != nil {
			return nil, fmt.Errorf("get ready work: compute deferred parent children: %w", dcErr)
		}
		if len(deferredChildIDs) > 0 {
			for start := 0; start < len(deferredChildIDs); start += queryBatchSize {
				end := start + queryBatchSize
				if end > len(deferredChildIDs) {
					end = len(deferredChildIDs)
				}
				placeholders, batchArgs := buildInPlaceholders(deferredChildIDs[start:end])
				args = append(args, batchArgs...)
				whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (%s)", placeholders))
			}
		}
	}

	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label = ?)", tables.Labels))
			args = append(args, label)
		}
	}
	if len(filter.ExcludeLabels) > 0 {
		ph, a := buildInPlaceholders(filter.ExcludeLabels)
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE label IN (%s))", tables.Labels, ph))
		args = append(args, a...)
	}

	if filter.ParentID != nil {
		parentID := *filter.ParentID
		descendantIDs, descErr := r.getDescendantIDs(ctx, parentID, 0)
		if descErr != nil {
			return nil, fmt.Errorf("get parent descendants: %w", descErr)
		}
		parentClauses := []string{fmt.Sprintf("(id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child'))", tables.Dependencies)}
		args = append(args, parentID)
		for start := 0; start < len(descendantIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(descendantIDs) {
				end = len(descendantIDs)
			}
			placeholders, batchArgs := buildInPlaceholders(descendantIDs[start:end])
			parentClauses = append(parentClauses, fmt.Sprintf("id IN (%s)", placeholders))
			args = append(args, batchArgs...)
		}
		whereClauses = append(whereClauses, "("+strings.Join(parentClauses, " OR ")+")")
	}

	if filter.MoleculeID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("(id IN (SELECT issue_id FROM %s WHERE type = 'parent-child' AND %s = ?) OR (id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')))", tables.Dependencies, depTargetExpr, tables.Dependencies))
		args = append(args, filter.MoleculeID, filter.MoleculeID)
	}

	var metaErr error
	whereClauses, args, metaErr = appendMetadataClauses(whereClauses, args, filter.HasMetadataKey, filter.MetadataFields)
	if metaErr != nil {
		return nil, metaErr
	}

	return &readyWorkPredicates{
		whereSQL: "WHERE " + strings.Join(whereClauses, " AND "),
		args:     args,
	}, nil
}

type deferredParentEdge struct {
	depTable, issueTable, targetCol string
}

var deferredParentEdges = []deferredParentEdge{
	{"dependencies", "issues", "depends_on_issue_id"},
	{"dependencies", "wisps", "depends_on_wisp_id"},
	{"wisp_dependencies", "issues", "depends_on_issue_id"},
	{"wisp_dependencies", "wisps", "depends_on_wisp_id"},
}

func (r *issueSQLRepositoryImpl) getChildrenOfDeferredParents(ctx context.Context) ([]string, error) {
	has, err := r.anyFutureDeferredParent(ctx)
	if err != nil || !has {
		return nil, err
	}
	return r.descendantsOfFutureDeferredParents(ctx)
}

func (r *issueSQLRepositoryImpl) anyFutureDeferredParent(ctx context.Context) (bool, error) {
	for _, table := range []string{"issues", "wisps"} {
		var probe int
		//nolint:gosec // G201: table is a hardcoded constant.
		err := r.runner.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT 1 FROM %s WHERE defer_until IS NOT NULL AND defer_until > UTC_TIMESTAMP() LIMIT 1`,
			table)).Scan(&probe)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, sql.ErrNoRows), dberrors.IsTableNotExist(err):
			continue
		default:
			return false, fmt.Errorf("deferred parents: check %s: %w", table, err)
		}
	}
	return false, nil
}

func (r *issueSQLRepositoryImpl) descendantsOfFutureDeferredParents(ctx context.Context) ([]string, error) {
	var childIDs []string
	for _, e := range deferredParentEdges {
		//nolint:gosec // G201: depTable/issueTable/targetCol are hardcoded.
		q := fmt.Sprintf(`
			SELECT dep.issue_id
			FROM %s dep
			JOIN %s parent ON parent.id = dep.%s
			WHERE dep.type = 'parent-child'
			  AND parent.defer_until IS NOT NULL
			  AND parent.defer_until > UTC_TIMESTAMP()
		`, e.depTable, e.issueTable, e.targetCol)
		rows, err := r.runner.QueryContext(ctx, q)
		if err != nil {
			if dberrors.IsTableNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("deferred parents: %s/%s: %w", e.depTable, e.issueTable, err)
		}
		if err := scanStringsInto(rows, &childIDs); err != nil {
			return nil, fmt.Errorf("deferred parents: %s/%s: %w", e.depTable, e.issueTable, err)
		}
	}
	return childIDs, nil
}

func scanStringsInto(rows *sql.Rows, out *[]string) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		*out = append(*out, s)
	}
	return rows.Err()
}

//nolint:gosec // G201: depTable is hardcoded.
func (r *issueSQLRepositoryImpl) getDescendantIDs(ctx context.Context, rootID string, maxDepth int) ([]string, error) {
	if rootID == "" {
		return nil, nil
	}

	queryDescendants := func(includeWisps bool) ([]string, bool, error) {
		edgeQuery := fmt.Sprintf(`
			SELECT issue_id, %s FROM dependencies WHERE type = 'parent-child'
		`, depTargetExpr)
		if includeWisps {
			edgeQuery += fmt.Sprintf(`
			UNION ALL
			SELECT issue_id, %s FROM wisp_dependencies WHERE type = 'parent-child'
		`, depTargetExpr)
		}

		//nolint:gosec // G201: edgeQuery is built from hardcoded SQL plus depTargetExpr (no user input)
		query := fmt.Sprintf(`
			WITH RECURSIVE
			parent_edges(issue_id, depends_on_id) AS (
				%s
			),
			descendants(id, depth, path) AS (
				SELECT issue_id, 1, CONCAT(',', ?, ',', issue_id, ',')
				FROM parent_edges
				WHERE depends_on_id = ?
				UNION ALL
				SELECT e.issue_id, d.depth + 1, CONCAT(d.path, e.issue_id, ',')
				FROM parent_edges e
				JOIN descendants d ON e.depends_on_id = d.id
				WHERE (? <= 0 OR d.depth < ?)
				  AND LOCATE(CONCAT(',', e.issue_id, ','), d.path) = 0
			)
			SELECT id, depth FROM descendants WHERE id <> ?
		`, edgeQuery)

		rows, err := r.runner.QueryContext(ctx, query, rootID, rootID, maxDepth, maxDepth, rootID)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = rows.Close() }()

		var result []string
		reachedMaxDepth := false
		for rows.Next() {
			var id string
			var depth int
			if err := rows.Scan(&id, &depth); err != nil {
				return nil, false, fmt.Errorf("scan descendant: %w", err)
			}
			result = append(result, id)
			if maxDepth > 0 && depth >= maxDepth {
				reachedMaxDepth = true
			}
		}
		if err := rows.Err(); err != nil {
			return nil, false, fmt.Errorf("descendant rows: %w", err)
		}
		return result, reachedMaxDepth, nil
	}

	result, reachedMaxDepth, err := queryDescendants(true)
	if err != nil {
		if !dberrors.IsTableNotExist(err) {
			return nil, err
		}
		result, reachedMaxDepth, err = queryDescendants(false)
		if err != nil {
			return nil, err
		}
	}
	if reachedMaxDepth {
		return nil, fmt.Errorf("parent descendant traversal for %s reached max depth %d", rootID, maxDepth)
	}
	return result, nil
}
