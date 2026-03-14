package storage

import (
	"context"

	"github.com/gastownhall/beads/internal/types"
)

// BulkIssueStore provides extended issue CRUD beyond the base Storage interface.
type BulkIssueStore interface {
	CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts BatchCreateOptions) error
	DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error)
	DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error)
	UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error
	ClaimIssue(ctx context.Context, id string, actor string) error
	PromoteFromEphemeral(ctx context.Context, id string, actor string) error
	GetNextChildID(ctx context.Context, parentID string) (string, error)
	RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error
}
