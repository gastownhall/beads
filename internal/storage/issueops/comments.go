package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetIssueCommentsInTx retrieves comments for an issue within an existing
// transaction. Automatically routes to wisp_comments if the ID is an active wisp.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GetIssueCommentsInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.Comment, error) {
	table := "comments"
	if IsActiveWispInTx(ctx, tx, issueID) {
		table = "wisp_comments"
	}

	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at ASC, id ASC
	`, table), issueID)
	if err != nil {
		return nil, fmt.Errorf("get issue comments from %s: %w", table, err)
	}
	defer rows.Close()

	var comments []*types.Comment
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("get issue comments: scan: %w", err)
		}
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

// GetCommentCountsInTx returns comment counts per issue ID within a transaction.
// Routes each ID to comments or wisp_comments based on wisp status.
// Uses batched IN clauses (queryBatchSize) to avoid query-planner spikes.
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
		for start := 0; start < len(pair.ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(pair.ids) {
				end = len(pair.ids)
			}
			batch := pair.ids[start:end]
			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for i, id := range batch {
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
	}

	return result, nil
}
