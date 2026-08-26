package issueops

import (
	"context"
	"fmt"
	"sort"
)

type journalDependencyEdge struct {
	source   string
	target   string
	kind     string
	metadata string
	isWisp   bool
}

// RecordDependencyRemovalsForIssuesInTx emits one deterministic dep_remove
// record for every edge whose source or target is in ids. Callers invoke this
// before deleting the edges or nodes so source snapshots are still available.
func RecordDependencyRemovalsForIssuesInTx(ctx context.Context, tx DBTX, ids []string) error {
	if !journalEnabled(ctx, tx) || len(ids) == 0 {
		return nil
	}
	edges, err := dependencyEdgesForIssueIDsInTx(ctx, tx, ids)
	if err != nil {
		return err
	}
	return recordDependencyRemovalsInTx(ctx, tx, edges)
}

// RecordDependencyRemovalsForIssuePlanesInTx emits dep_remove records for the
// exact issue rows a plane-resolved delete is about to remove. A dual-resident
// ID can own distinct outgoing edges in dependencies and wisp_dependencies;
// selecting its wisp must not report removals for the untouched durable twin.
// Incoming edges are selected by their typed target column for the same reason.
func RecordDependencyRemovalsForIssuePlanesInTx(
	ctx context.Context,
	tx DBTX,
	issueIDs, wispIDs []string,
) error {
	if !journalEnabled(ctx, tx) || (len(issueIDs) == 0 && len(wispIDs) == 0) {
		return nil
	}

	byKey := make(map[string]journalDependencyEdge)
	queries := []struct {
		table  string
		column string
		ids    []string
	}{
		// Outgoing edges live in the source row's plane.
		{table: "dependencies", column: "issue_id", ids: issueIDs},
		{table: "wisp_dependencies", column: "issue_id", ids: wispIDs},
		// Incoming edges can originate in either plane, but their typed target
		// column records which twin they actually point at.
		{table: "dependencies", column: "depends_on_issue_id", ids: issueIDs},
		{table: "wisp_dependencies", column: "depends_on_issue_id", ids: issueIDs},
		{table: "dependencies", column: "depends_on_wisp_id", ids: wispIDs},
		{table: "wisp_dependencies", column: "depends_on_wisp_id", ids: wispIDs},
	}
	for _, query := range queries {
		edges, err := dependencyEdgesInTableForColumnIDsInTx(ctx, tx, query.table, query.column, query.ids)
		if err != nil {
			return err
		}
		for _, edge := range edges {
			byKey[dependencyEdgeKey(edge)] = edge
		}
	}
	return recordDependencyRemovalsInTx(ctx, tx, sortedDependencyEdges(byKey))
}

// RecordDependencyRemovalsForTableInTx is the table-scoped variant used by
// the UOW dependency repository immediately before its bulk edge DELETE.
func RecordDependencyRemovalsForTableInTx(ctx context.Context, tx DBTX, table string, ids []string) error {
	if !journalEnabled(ctx, tx) || len(ids) == 0 {
		return nil
	}
	edges, err := dependencyEdgesInTableForIssueIDsInTx(ctx, tx, table, ids)
	if err != nil {
		return err
	}
	return recordDependencyRemovalsInTx(ctx, tx, edges)
}

func recordDependencyRemovalsInTx(ctx context.Context, tx DBTX, edges []journalDependencyEdge) error {
	for _, edge := range edges {
		// Every caller is bulk/cascade delete plumbing (node deletes, source-repo
		// wipes, the UOW bulk edge DELETE), none of which carries an actor.
		if err := RecordDepEventForPlaneInTx(ctx, tx, EventDepRemove, edge.source, edge.kind, edge.target, edge.metadata, "", edge.isWisp); err != nil {
			return err
		}
	}
	return nil
}

func dependencyEdgesForIssueIDsInTx(ctx context.Context, tx DBTX, ids []string) ([]journalDependencyEdge, error) {
	byKey := make(map[string]journalDependencyEdge)
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		edges, err := dependencyEdgesInTableForIssueIDsInTx(ctx, tx, table, ids)
		if err != nil {
			return nil, err
		}
		for _, edge := range edges {
			byKey[dependencyEdgeKey(edge)] = edge
		}
	}
	return sortedDependencyEdges(byKey), nil
}

func dependencyEdgesInTableForIssueIDsInTx(ctx context.Context, tx DBTX, table string, ids []string) ([]journalDependencyEdge, error) {
	switch table {
	case "dependencies", "wisp_dependencies":
	default:
		return nil, fmt.Errorf("journal: unsupported dependency table %q", table)
	}

	byKey := make(map[string]journalDependencyEdge)
	for start := 0; start < len(ids); start += deleteBatchSize {
		end := start + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		inClause, args := buildSQLInClause(ids[start:end])
		queryArgs := append(append([]any{}, args...), args...)
		//nolint:gosec // table is validated above and inClause contains only placeholders.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, %s AS target, type, metadata
			FROM %s
			WHERE issue_id IN (%s) OR %s IN (%s)
		`, DepTargetExpr, table, inClause, DepTargetExpr, inClause), queryArgs...)
		if err != nil {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return nil, fmt.Errorf("journal: query dependency removals from %s: %w", table, err)
		}
		for rows.Next() {
			edge := journalDependencyEdge{isWisp: table == "wisp_dependencies"}
			if err := rows.Scan(&edge.source, &edge.target, &edge.kind, &edge.metadata); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("journal: scan dependency removal from %s: %w", table, err)
			}
			byKey[dependencyEdgeKey(edge)] = edge
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("journal: iterate dependency removals from %s: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("journal: close dependency removals from %s: %w", table, err)
		}
	}
	return sortedDependencyEdges(byKey), nil
}

func dependencyEdgesInTableForColumnIDsInTx(
	ctx context.Context,
	tx DBTX,
	table, column string,
	ids []string,
) ([]journalDependencyEdge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	switch table {
	case "dependencies", "wisp_dependencies":
	default:
		return nil, fmt.Errorf("journal: unsupported dependency table %q", table)
	}
	switch column {
	case "issue_id", "depends_on_issue_id", "depends_on_wisp_id":
	default:
		return nil, fmt.Errorf("journal: unsupported dependency column %q", column)
	}

	byKey := make(map[string]journalDependencyEdge)
	for start := 0; start < len(ids); start += deleteBatchSize {
		end := start + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		inClause, args := buildSQLInClause(ids[start:end])
		//nolint:gosec // table and column are validated above; inClause contains only placeholders.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, %s AS target, type, metadata
			FROM %s
			WHERE %s IN (%s)
		`, DepTargetExpr, table, column, inClause), args...)
		if err != nil {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return nil, fmt.Errorf("journal: query dependency removals from %s by %s: %w", table, column, err)
		}
		for rows.Next() {
			edge := journalDependencyEdge{isWisp: table == "wisp_dependencies"}
			if err := rows.Scan(&edge.source, &edge.target, &edge.kind, &edge.metadata); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("journal: scan dependency removal from %s by %s: %w", table, column, err)
			}
			byKey[dependencyEdgeKey(edge)] = edge
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("journal: iterate dependency removals from %s by %s: %w", table, column, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("journal: close dependency removals from %s by %s: %w", table, column, err)
		}
	}
	return sortedDependencyEdges(byKey), nil
}

func dependencyEdgeKey(edge journalDependencyEdge) string {
	plane := "issue"
	if edge.isWisp {
		plane = "wisp"
	}
	return plane + "\x00" + edge.source + "\x00" + edge.target + "\x00" + edge.kind + "\x00" + edge.metadata
}

func sortedDependencyEdges(byKey map[string]journalDependencyEdge) []journalDependencyEdge {
	edges := make([]journalDependencyEdge, 0, len(byKey))
	for _, edge := range byKey {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].source != edges[j].source {
			return edges[i].source < edges[j].source
		}
		if edges[i].target != edges[j].target {
			return edges[i].target < edges[j].target
		}
		if edges[i].kind != edges[j].kind {
			return edges[i].kind < edges[j].kind
		}
		if edges[i].metadata != edges[j].metadata {
			return edges[i].metadata < edges[j].metadata
		}
		return !edges[i].isWisp && edges[j].isWisp
	})
	return edges
}
