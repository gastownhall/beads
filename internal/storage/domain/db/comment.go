package db

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
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
	table := pickCommentTable(opts.UseWispsTable)
	err := forEachIDBatch(issueIDs, func(batch []string) error {
		placeholders, args := buildInPlaceholders(batch)
		//nolint:gosec // G201: table is one of two hardcoded constants
		q := fmt.Sprintf(
			"SELECT issue_id, COUNT(*) FROM %s WHERE issue_id IN (%s) GROUP BY issue_id",
			table, placeholders,
		)
		rows, err := r.runner.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("db: CommentSQLRepository.CountsByIssueIDs: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var issueID string
			var count int
			if err := rows.Scan(&issueID, &count); err != nil {
				return fmt.Errorf("db: CommentSQLRepository.CountsByIssueIDs: scan: %w", err)
			}
			result[issueID] = count
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("db: CommentSQLRepository.CountsByIssueIDs: rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *commentSQLRepositoryImpl) ListByIssueIDs(ctx context.Context, issueIDs []string, opts domain.CommentOpts) (map[string][]*types.Comment, error) {
	result := make(map[string][]*types.Comment)
	table := pickCommentTable(opts.UseWispsTable)
	err := forEachIDBatch(issueIDs, func(batch []string) error {
		placeholders, args := buildInPlaceholders(batch)
		//nolint:gosec // G201: table is one of two hardcoded constants
		q := fmt.Sprintf(`
			SELECT id, issue_id, author, text, created_at
			FROM %s
			WHERE issue_id IN (%s)
			ORDER BY issue_id, created_at ASC, id ASC
		`, table, placeholders)
		rows, err := r.runner.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("db: CommentSQLRepository.ListByIssueIDs: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var c types.Comment
			if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
				return fmt.Errorf("db: CommentSQLRepository.ListByIssueIDs: scan: %w", err)
			}
			cc := c
			result[c.IssueID] = append(result[c.IssueID], &cc)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("db: CommentSQLRepository.ListByIssueIDs: rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
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

func (r *commentSQLRepositoryImpl) Insert(ctx context.Context, issueID, author, text string, opts domain.CommentOpts) (*types.Comment, error) {
	// Live add: advance past the issue's newest comment so a burst inside one
	// second still reads back in write order (issueops.NextLiveCommentTime).
	// InsertRecord honors a supplied CreatedAt verbatim, which is what keeps
	// imported comments on their original timestamps.
	stamp, err := issueops.NextLiveCommentTime(ctx, r.runner, pickCommentTable(opts.UseWispsTable), issueID, time.Now())
	if err != nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.Insert: %w", err)
	}
	return r.InsertRecord(ctx, &types.Comment{IssueID: issueID, Author: author, Text: text, CreatedAt: stamp}, opts)
}

func (r *commentSQLRepositoryImpl) InsertRecord(ctx context.Context, comment *types.Comment, opts domain.CommentOpts) (*types.Comment, error) {
	if comment == nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.InsertRecord: comment must not be nil")
	}
	copy := *comment
	if copy.IssueID == "" {
		return nil, fmt.Errorf("db: CommentSQLRepository.InsertRecord: issueID must not be empty")
	}

	issueTable := pickIssueTable(opts.UseWispsTable)
	var exists bool
	//nolint:gosec // G201: issueTable is one of two hardcoded constants
	if err := r.runner.QueryRowContext(ctx,
		fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)", issueTable), copy.IssueID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.InsertRecord: check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("db: CommentSQLRepository.InsertRecord: issue %s not found", copy.IssueID)
	}

	if copy.CreatedAt.IsZero() {
		copy.CreatedAt = time.Now().UTC()
	} else {
		copy.CreatedAt = copy.CreatedAt.UTC()
	}
	createdAtText := issueops.FormatAuxTime(copy.CreatedAt)
	commentTable := pickCommentTable(opts.UseWispsTable)
	if copy.ID == "" {
		id, _, err := issueops.InsertDerivedComment(ctx, r.runner, commentTable, copy.IssueID, copy.Author, copy.Text, createdAtText)
		if err != nil {
			return nil, fmt.Errorf("db: CommentSQLRepository.InsertRecord: %w", err)
		}
		copy.ID = id
	} else {
		//nolint:gosec // G201: commentTable is one of two hardcoded constants
		if _, err := r.runner.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (id, issue_id, author, text, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, commentTable), copy.ID, copy.IssueID, copy.Author, copy.Text, createdAtText); err != nil {
			return nil, fmt.Errorf("db: CommentSQLRepository.InsertRecord: %w", err)
		}
	}
	createdAt, err := issueops.ParseAuxTime(createdAtText)
	if err != nil {
		return nil, fmt.Errorf("db: CommentSQLRepository.InsertRecord: %w", err)
	}
	copy.CreatedAt = createdAt

	if err := issueops.RecordCommentEventInTx(ctx, r.runner, copy.IssueID, &issueops.EventComment{
		ID: copy.ID, Author: copy.Author, Text: copy.Text, CreatedAt: copy.CreatedAt, Source: issueops.CommentSourceStructured,
	}); err != nil {
		return nil, err
	}
	return &copy, nil
}
