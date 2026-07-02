package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func NewCommentSQLRepository(runner Runner) domain.CommentSQLRepository {
	return &commentSQLRepositoryImpl{runner: runner}
}

type commentSQLRepositoryImpl struct {
	runner Runner
}

var _ domain.CommentSQLRepository = (*commentSQLRepositoryImpl)(nil)

func pickCommentTable(useWisps bool) string {
	if useWisps {
		return "wisp_comments"
	}
	return "comments"
}

func (r *commentSQLRepositoryImpl) CountsByIssueIDs(ctx context.Context, issueIDs []string, opts domain.CommentOpts) (map[string]int, error) {
	result := make(map[string]int)
	if len(issueIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(issueIDs))
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	table := pickCommentTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	q := fmt.Sprintf(
		"SELECT issue_id, COUNT(*) FROM %s WHERE issue_id IN (%s) GROUP BY issue_id",
		table, strings.Join(placeholders, ","),
	)
	rows, err := r.runner.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.CountsByIssueIDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var issueID string
		var count int
		if err := rows.Scan(&issueID, &count); err != nil {
			return nil, fmt.Errorf("db: CommentSQLRepository.CountsByIssueIDs: scan: %w", err)
		}
		result[issueID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.CountsByIssueIDs: rows: %w", err)
	}
	return result, nil
}

func (r *commentSQLRepositoryImpl) ListByIssueIDs(ctx context.Context, issueIDs []string, opts domain.CommentOpts) (map[string][]*types.Comment, error) {
	result := make(map[string][]*types.Comment)
	if len(issueIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(issueIDs))
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	table := pickCommentTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	q := fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id IN (%s)
		ORDER BY issue_id, created_at ASC, id ASC
	`, table, strings.Join(placeholders, ","))
	rows, err := r.runner.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.ListByIssueIDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("db: CommentSQLRepository.ListByIssueIDs: scan: %w", err)
		}
		cc := c
		result[c.IssueID] = append(result[c.IssueID], &cc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.ListByIssueIDs: rows: %w", err)
	}
	return result, nil
}

func (r *commentSQLRepositoryImpl) IterByIssueID(ctx context.Context, issueID string, opts domain.CommentOpts) (storage.Iter[types.Comment], error) {
	bulk, err := r.ListByIssueIDs(ctx, []string{issueID}, opts)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(bulk[issueID]), nil
}
