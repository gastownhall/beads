package uow

import (
	"context"
	"fmt"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// CommenterSource is the capability accessor a unit-of-work provider offers
// for the comment role, the sibling of IssueLifecycleSource and
// DependencyEditorSource.
type CommenterSource interface {
	Commenter() (publicops.Commenter, error)
}

// commenter appends comments through a unit of work.
type commenter struct {
	provider UnitOfWorkProvider
}

// Commenter returns the guarded add-comment surface for this provider.
func (p *doltSQLProvider) Commenter() (publicops.Commenter, error) {
	return NewCommenter(p)
}

// NewCommenter constructs a public commenter backed by provider.
func NewCommenter(provider UnitOfWorkProvider) (publicops.Commenter, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new commenter: unit-of-work provider must not be nil")
	}
	return &commenter{provider: provider}, nil
}

var _ publicops.Commenter = (*commenter)(nil)

// AddComment resolves the plane and appends the row in ONE unit of work.
//
// The resolve is workapi.GetIssueOrWisp — the same issue-then-wisp fallback
// Reader.Get runs, through the same function, so the two roles cannot come to
// disagree about which plane an id names. It runs INSIDE the transaction the
// insert runs in, so a comment cannot land on a row an earlier read saw.
func (c *commenter) AddComment(ctx context.Context, request publicops.AddCommentRequest) (publicops.AddCommentResult, error) {
	if err := storageissueops.ValidateAddCommentRequest(request); err != nil {
		return publicops.AddCommentResult{}, err
	}
	return RunTxResult(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.AddCommentResult, string, error) {
		issue, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), request.IssueID)
		if err != nil {
			return publicops.AddCommentResult{}, "", err
		}
		var comment *publicops.Comment
		if isWisp {
			comment, err = uw.CommentUseCase().AddCommentToWisp(ctx, issue.ID, request.Author, request.Text)
		} else {
			comment, err = uw.CommentUseCase().AddCommentToIssue(ctx, issue.ID, request.Author, request.Text)
		}
		if err != nil {
			return publicops.AddCommentResult{}, "", err
		}
		return publicops.AddCommentResult{Comment: comment},
			storageissueops.AddCommentCommitMessage(issue.ID), nil
	})
}
