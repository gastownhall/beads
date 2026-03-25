package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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
		SELECT id, issue_id, author, text, created_at,
		       COALESCE(external_ref, '') AS external_ref,
		       COALESCE(updated_at, created_at) AS updated_at
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
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt, &c.ExternalRef, &c.UpdatedAt); err != nil {
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

// AddIssueCommentInTx adds a structured comment to an issue within a transaction.
// Routes to comments or wisp_comments based on wisp status.
//
//nolint:gosec // G201: table names come from hardcoded constants
func AddIssueCommentInTx(ctx context.Context, tx *sql.Tx, issueID, author, text string) (*types.Comment, error) {
	return ImportIssueCommentInTx(ctx, tx, issueID, author, text, time.Now().UTC())
}

// ImportIssueCommentInTx adds a comment preserving the original timestamp.
//
//nolint:gosec // G201: table names come from hardcoded constants
func ImportIssueCommentInTx(ctx context.Context, tx *sql.Tx, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	issueTable, _, _, _ := WispTableRouting(isWisp)
	commentTable := "comments"
	if isWisp {
		commentTable = "wisp_comments"
	}

	// Verify issue exists.
	var exists bool
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)`, issueTable), issueID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	createdAt = createdAt.UTC()
	id := uuid.Must(uuid.NewV7()).String()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, commentTable), id, issueID, author, text, createdAt); err != nil {
		return nil, fmt.Errorf("add comment to %s: %w", commentTable, err)
	}

	return &types.Comment{
		ID:        id,
		IssueID:   issueID,
		Author:    author,
		Text:      text,
		CreatedAt: createdAt,
	}, nil
}

// GetCommentByExternalRefInTx retrieves a comment by its external_ref within a transaction.
// Returns nil, nil if not found.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetCommentByExternalRefInTx(ctx context.Context, tx *sql.Tx, issueID, externalRef string) (*types.Comment, error) {
	if externalRef == "" {
		return nil, nil
	}

	table := "comments"
	if IsActiveWispInTx(ctx, tx, issueID) {
		table = "wisp_comments"
	}

	var c types.Comment
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at,
		       COALESCE(external_ref, '') AS external_ref,
		       COALESCE(updated_at, created_at) AS updated_at
		FROM %s
		WHERE issue_id = ? AND external_ref = ?
	`, table), issueID, externalRef).Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt, &c.ExternalRef, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get comment by external_ref from %s: %w", table, err)
	}
	return &c, nil
}

// ImportIssueCommentWithRefInTx adds a comment preserving its external_ref and original timestamp.
//
//nolint:gosec // G201: table names come from hardcoded constants
func ImportIssueCommentWithRefInTx(ctx context.Context, tx *sql.Tx, issueID, author, text, externalRef string, createdAt time.Time) (*types.Comment, error) {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	issueTable, _, _, _ := WispTableRouting(isWisp)
	commentTable := "comments"
	if isWisp {
		commentTable = "wisp_comments"
	}

	// Verify issue exists.
	var exists bool
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)`, issueTable), issueID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	createdAt = createdAt.UTC()
	id := uuid.Must(uuid.NewV7()).String()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, author, text, external_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, commentTable), id, issueID, author, text, externalRef, createdAt); err != nil {
		return nil, fmt.Errorf("add comment with ref to %s: %w", commentTable, err)
	}

	return &types.Comment{
		ID:          id,
		IssueID:     issueID,
		Author:      author,
		Text:        text,
		ExternalRef: externalRef,
		CreatedAt:   createdAt,
	}, nil
}

// UpdateCommentExternalRefInTx sets the external_ref on an existing comment.
//
//nolint:gosec // G201: table names come from hardcoded constants
func UpdateCommentExternalRefInTx(ctx context.Context, tx *sql.Tx, issueID, commentID, externalRef string) error {
	table := "comments"
	if IsActiveWispInTx(ctx, tx, issueID) {
		table = "wisp_comments"
	}

	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET external_ref = ? WHERE id = ? AND issue_id = ?
	`, table), externalRef, commentID, issueID)
	if err != nil {
		return fmt.Errorf("update comment external_ref in %s: %w", table, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("comment %s not found in issue %s", commentID, issueID)
	}
	return nil
}

// AddCommentEventInTx adds a comment as an event to an issue within a transaction.
// Routes to events or wisp_events based on wisp status.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func AddCommentEventInTx(ctx context.Context, tx *sql.Tx, issueID, actor, comment string) error {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	_, _, eventTable, _ := WispTableRouting(isWisp)

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, eventTable), issueID, types.EventCommented, actor, comment); err != nil {
		return fmt.Errorf("add comment event to %s: %w", eventTable, err)
	}
	return nil
}
