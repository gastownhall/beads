package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// GetCommentCountsInTx returns comment counts per issue ID within a transaction.
// Routes each ID to comments or wisp_comments based on wisp status.
func GetCommentCountsInTx(ctx context.Context, tx *sql.Tx, issueIDs []string) (map[string]int, error) {
	if len(issueIDs) == 0 {
		return make(map[string]int), nil
	}

	result := make(map[string]int)

	var wispIDs, permIDs []string
	for _, id := range issueIDs {
		if IsActiveWispInTx(ctx, tx, id) {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}

	for _, pair := range []struct {
		table string
		ids   []string
	}{
		{"wisp_comments", wispIDs},
		{"comments", permIDs},
	} {
		if len(pair.ids) == 0 {
			continue
		}
		placeholders := make([]string, len(pair.ids))
		args := make([]any, len(pair.ids))
		for i, id := range pair.ids {
			placeholders[i] = "?"
			args[i] = id
		}
		//nolint:gosec // G201: pair.table is hardcoded
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, COUNT(*) as cnt FROM %s WHERE issue_id IN (%s) GROUP BY issue_id`,
			pair.table, strings.Join(placeholders, ",")), args...)
		if err != nil {
			return nil, fmt.Errorf("get comment counts from %s: %w", pair.table, err)
		}
		for rows.Next() {
			var issueID string
			var count int
			if err := rows.Scan(&issueID, &count); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get comment counts: scan: %w", err)
			}
			result[issueID] = count
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get comment counts: rows: %w", err)
		}
	}

	return result, nil
}
