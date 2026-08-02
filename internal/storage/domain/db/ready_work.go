package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

type readyWorkPredicates struct {
	whereSQL string
	args     []any
}

// buildReadyWorkOrder orders by the sort_* aliases projected by
// sqlbuild.UnionSortColumnsSQL, since ready work always sorts at the UNION
// outer query here.
func buildReadyWorkOrder(policy types.SortPolicy) sqlbuild.ReadyWorkOrder {
	return sqlbuild.BuildReadyWorkOrder(policy, "sort_created", "sort_priority")
}

// buildReadyWorkPredicates computes the ID sets the ready-work WHERE clause
// needs (children of deferred parents, parent descendants), then delegates
// the clause text to sqlbuild so both stacks share ready semantics. Unlike
// the classic stack, ORDER BY and LIMIT are applied at the UNION outer query.
func (r *issueSQLRepositoryImpl) buildReadyWorkPredicates(ctx context.Context, filter types.WorkFilter, tables filterTables) (*readyWorkPredicates, error) {
	var inputs sqlbuild.ReadyWorkWhereInputs
	if !filter.IncludeDeferred {
		deferredChildIDs, dcErr := r.getChildrenOfDeferredParents(ctx)
		if dcErr != nil {
			return nil, fmt.Errorf("get ready work: compute deferred parent children: %w", dcErr)
		}
		inputs.DeferredChildIDs = deferredChildIDs
	}
	if filter.ParentID != nil {
		descendantIDs, descErr := r.getDescendantIDs(ctx, *filter.ParentID, 0)
		if descErr != nil {
			return nil, fmt.Errorf("get parent descendants: %w", descErr)
		}
		inputs.ParentDescendantIDs = descendantIDs
	}

	whereSQL, args, err := sqlbuild.BuildReadyWorkWhere(filter, tables, inputs)
	if err != nil {
		return nil, err
	}
	return &readyWorkPredicates{whereSQL: whereSQL, args: args}, nil
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
