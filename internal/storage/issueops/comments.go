package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/storage"
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

// Comment page-read tuning. Mirrors the EventsSince keyset clamp: an unbounded
// page defeats the purpose of paging a long thread, so a non-positive limit
// falls back to the default and any larger request is capped.
const (
	defaultCommentsPageLimit = 100
	maxCommentsPageLimit     = 500
)

// CommentsKeysetPredicate is the SARGABLE (created_at ASC, id ASC) keyset resume
// predicate GetIssueCommentsPageInTx ANDs in once a page cursor is set. Its three
// ? placeholders bind, in order: created_at (the sargable lower bound),
// created_at (strict), and id (the same-second tie-break).
//
// It is logically "(created_at, id) is strictly after the cursor" under
// created_at ASC, id ASC — i.e. (created_at > ?) OR (created_at = ? AND id > ?)
// — but rewritten with a redundant `created_at >= ?` leading bound so the
// planner seeks the (issue_id, created_at, id) index (an IndexedTableAccess
// range on Dolt) instead of scanning the issue's comments and filtering. The two
// forms select the same rows: created_at >= C is true whenever the OR is, and
// prunes only created_at < C, which the OR already excludes. It is exported so
// the backend sargability guard EXPLAINs this exact string rather than a
// hand-copied literal — a change here then breaks the guard.
const CommentsKeysetPredicate = `created_at >= ? AND ((created_at > ?) OR (id > ?))`

// CommentsPageQuery returns the exact SQL GetIssueCommentsPageInTx executes for
// the given comment table (comments or wisp_comments), cursor presence, and
// already-clamped limit. The ? placeholders bind in order: issue_id, then — when
// hasCursor — the three CommentsKeysetPredicate binds (created_at, created_at,
// id). It is exported so the sargability guard EXPLAINs this production string
// (with CommentsKeysetPredicate embedded) rather than a copy — a drift in either
// the surrounding query or the predicate then breaks the guard.
//
//nolint:gosec // G201: table is a hardcoded routing constant and limit is an int the caller clamps; every runtime value is a bound parameter.
func CommentsPageQuery(table string, hasCursor bool, limit int) string {
	query := fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id = ?`, table)
	if hasCursor {
		query += " AND " + CommentsKeysetPredicate
	}
	query += fmt.Sprintf(" ORDER BY created_at ASC, id ASC LIMIT %d", limit)
	return query
}

// GetIssueCommentsPageInTx returns one keyset page of an issue's comments within
// an existing transaction, ordered by (created_at ASC, id ASC). It routes to
// wisp_comments when issueID is an active wisp, exactly like GetIssueCommentsInTx.
//
// after is the resume position — the (created_at, id) of the last comment already
// seen. The zero cursor starts from the beginning of the thread. Walking the
// pages, feeding each page's last (created_at, id) back in as after, yields
// exactly the same comments in the same order as GetIssueCommentsInTx, with no
// dropped or duplicated comment even when several comments share a created_at
// second, because id (the primary key) is a total tie-break.
//
// The cursor must come from a previously returned comment: created_at is a
// DATETIME(0) column, so a sub-second cursor CreatedAt sorts after same-second
// rows stored at the truncated second and skips them on resume (AddIssueCommentInTx
// truncates its returned CreatedAt to avoid exactly this). And like an audit
// feed, a comment inserted with a backdated created_at behind an in-progress
// cursor is not seen by that forward-only walk.
//
// limit <= 0 falls back to defaultCommentsPageLimit (100); a larger limit is
// capped at maxCommentsPageLimit (500), so a caller that pages until
// len(page) < limit must keep limit <= 500 or terminate on an empty page
// instead. A missing issue routes to comments and returns an empty page with no
// error, matching GetIssueCommentsInTx.
func GetIssueCommentsPageInTx(ctx context.Context, tx *sql.Tx, issueID string, after storage.CommentPageCursor, limit int) ([]*types.Comment, error) {
	if limit <= 0 {
		limit = defaultCommentsPageLimit
	}
	if limit > maxCommentsPageLimit {
		limit = maxCommentsPageLimit
	}

	table := "comments"
	if IsActiveWispInTx(ctx, tx, issueID) {
		table = "wisp_comments"
	}

	hasCursor := !after.CreatedAt.IsZero() || after.ID != ""
	args := []any{issueID}
	if hasCursor {
		// Bind the cursor time as time.Time, not a formatted string: created_at
		// is a DATETIME column, so a time.Time value compares correctly on every
		// backend while an RFC3339 string can mis-compare. Bound twice (the
		// sargable lower bound and the strict bound), then the id tie-break.
		args = append(args, after.CreatedAt, after.CreatedAt, after.ID)
	}

	rows, err := tx.QueryContext(ctx, CommentsPageQuery(table, hasCursor, limit), args...)
	if err != nil {
		return nil, fmt.Errorf("get issue comments page from %s (after %v/%q): %w", table, after.CreatedAt, after.ID, err)
	}
	defer rows.Close()

	var comments []*types.Comment
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("get issue comments page: scan: %w", err)
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

	wispIDs, permIDs, err := PartitionWispIDsInTx(ctx, tx, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("partition comment issue IDs: %w", err)
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
// The insert timestamp is truncated to whole seconds to match the created_at
// column's DATETIME(0) precision, so the returned Comment.CreatedAt equals the
// stored value. That makes the returned comment safe to use directly as a
// GetIssueCommentsPage cursor: an un-truncated sub-second CreatedAt would sort
// after same-second rows stored at the truncated second and skip them on resume.
//
//nolint:gosec // G201: table names come from hardcoded constants
func AddIssueCommentInTx(ctx context.Context, tx *sql.Tx, issueID, author, text string) (*types.Comment, error) {
	return ImportIssueCommentInTx(ctx, tx, issueID, author, text, time.Now().UTC().Truncate(time.Second))
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

// AddCommentEventInTx adds a comment as an event to an issue within a transaction.
// Routes to events or wisp_events based on wisp status.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func AddCommentEventInTx(ctx context.Context, tx DBTX, issueID, actor, comment string) error {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	_, _, eventTable, _ := WispTableRouting(isWisp)

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?, ?)
	`, eventTable), NewEventID(), issueID, types.EventCommented, actor, comment); err != nil {
		return fmt.Errorf("add comment event to %s: %w", eventTable, err)
	}
	return nil
}
