package domain

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type CommentOpts struct {
	UseWispsTable bool
}

type CommentSQLRepository interface {
	CountsByIssueIDs(ctx context.Context, issueIDs []string, opts CommentOpts) (map[string]int, error)
	ListByIssueIDs(ctx context.Context, issueIDs []string, opts CommentOpts) (map[string][]*types.Comment, error)
	IterByIssueID(ctx context.Context, issueID string, opts CommentOpts) (storage.Iter[types.Comment], error)
	Insert(ctx context.Context, issueID, author, text string, opts CommentOpts) (*types.Comment, error)
}

type CommentUseCase interface {
	GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error)
	GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error)
	GetCommentsForIssue(ctx context.Context, issueID string) ([]*types.Comment, error)
	CountCommentsForIssue(ctx context.Context, issueID string) (int64, error)
	IterCommentsForIssue(ctx context.Context, issueID string) (storage.Iter[types.Comment], error)

	GetWispCommentCounts(ctx context.Context, wispIDs []string) (map[string]int, error)
	GetCommentsForWisps(ctx context.Context, wispIDs []string) (map[string][]*types.Comment, error)
	GetCommentsForWisp(ctx context.Context, wispID string) ([]*types.Comment, error)
	CountCommentsForWisp(ctx context.Context, wispID string) (int64, error)
	IterCommentsForWisp(ctx context.Context, wispID string) (storage.Iter[types.Comment], error)

	AddCommentToIssue(ctx context.Context, issueID, author, text string) (*types.Comment, error)
	AddCommentToWisp(ctx context.Context, wispID, author, text string) (*types.Comment, error)
}

func NewCommentUseCase(commentRepo CommentSQLRepository) CommentUseCase {
	return &commentUseCaseImpl{commentRepo: commentRepo}
}

type commentUseCaseImpl struct {
	commentRepo CommentSQLRepository
}

var _ CommentUseCase = (*commentUseCaseImpl)(nil)

func (u *commentUseCaseImpl) GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error) {
	return u.counts(ctx, issueIDs, false)
}

func (u *commentUseCaseImpl) GetWispCommentCounts(ctx context.Context, wispIDs []string) (map[string]int, error) {
	return u.counts(ctx, wispIDs, true)
}

func (u *commentUseCaseImpl) counts(ctx context.Context, ids []string, useWisp bool) (map[string]int, error) {
	if len(ids) == 0 {
		return map[string]int{}, nil
	}
	out, err := u.commentRepo.CountsByIssueIDs(ctx, ids, CommentOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("comment counts: %w", err)
	}
	return out, nil
}

func (u *commentUseCaseImpl) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	return u.list(ctx, issueIDs, false)
}

func (u *commentUseCaseImpl) GetCommentsForIssue(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return u.listOne(ctx, issueID, false)
}

func (u *commentUseCaseImpl) CountCommentsForIssue(ctx context.Context, issueID string) (int64, error) {
	return u.countOne(ctx, issueID, false)
}

func (u *commentUseCaseImpl) IterCommentsForIssue(ctx context.Context, issueID string) (storage.Iter[types.Comment], error) {
	return u.iterOne(ctx, issueID, false)
}

func (u *commentUseCaseImpl) GetCommentsForWisps(ctx context.Context, wispIDs []string) (map[string][]*types.Comment, error) {
	return u.list(ctx, wispIDs, true)
}

func (u *commentUseCaseImpl) GetCommentsForWisp(ctx context.Context, wispID string) ([]*types.Comment, error) {
	return u.listOne(ctx, wispID, true)
}

func (u *commentUseCaseImpl) CountCommentsForWisp(ctx context.Context, wispID string) (int64, error) {
	return u.countOne(ctx, wispID, true)
}

func (u *commentUseCaseImpl) IterCommentsForWisp(ctx context.Context, wispID string) (storage.Iter[types.Comment], error) {
	return u.iterOne(ctx, wispID, true)
}

func (u *commentUseCaseImpl) listOne(ctx context.Context, id string, useWisp bool) ([]*types.Comment, error) {
	if id == "" {
		return nil, fmt.Errorf("comment list: id must not be empty")
	}
	out, err := u.commentRepo.ListByIssueIDs(ctx, []string{id}, CommentOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("comment list: %w", err)
	}
	return out[id], nil
}

func (u *commentUseCaseImpl) countOne(ctx context.Context, id string, useWisp bool) (int64, error) {
	if id == "" {
		return 0, fmt.Errorf("comment count: id must not be empty")
	}
	out, err := u.commentRepo.CountsByIssueIDs(ctx, []string{id}, CommentOpts{UseWispsTable: useWisp})
	if err != nil {
		return 0, fmt.Errorf("comment count: %w", err)
	}
	return int64(out[id]), nil
}

func (u *commentUseCaseImpl) iterOne(ctx context.Context, id string, useWisp bool) (storage.Iter[types.Comment], error) {
	if id == "" {
		return nil, fmt.Errorf("comment iter: id must not be empty")
	}
	it, err := u.commentRepo.IterByIssueID(ctx, id, CommentOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("comment iter: %w", err)
	}
	return it, nil
}

func (u *commentUseCaseImpl) AddCommentToIssue(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	return u.add(ctx, issueID, author, text, false)
}

func (u *commentUseCaseImpl) AddCommentToWisp(ctx context.Context, wispID, author, text string) (*types.Comment, error) {
	return u.add(ctx, wispID, author, text, true)
}

func (u *commentUseCaseImpl) add(ctx context.Context, id, author, text string, useWisp bool) (*types.Comment, error) {
	if id == "" {
		return nil, fmt.Errorf("comment add: id must not be empty")
	}
	return u.commentRepo.Insert(ctx, id, author, text, CommentOpts{UseWispsTable: useWisp})
}

func (u *commentUseCaseImpl) list(ctx context.Context, ids []string, useWisp bool) (map[string][]*types.Comment, error) {
	if len(ids) == 0 {
		return map[string][]*types.Comment{}, nil
	}
	out, err := u.commentRepo.ListByIssueIDs(ctx, ids, CommentOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("comment list: %w", err)
	}
	return out, nil
}
