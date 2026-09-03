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
		if err := RecordDepEventInTx(ctx, tx, EventDepRemove, edge.source, edge.kind, edge.target, edge.metadata, ""); err != nil {
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
			var edge journalDependencyEdge
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

func dependencyEdgeKey(edge journalDependencyEdge) string {
	return edge.source + "\x00" + edge.target + "\x00" + edge.kind + "\x00" + edge.metadata
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
		return edges[i].kind < edges[j].kind
	})
	return edges
}
