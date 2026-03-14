package storage

import (
	"context"

	"github.com/gastownhall/beads/internal/types"
)

// DependencyQueryStore provides extended dependency queries beyond the base Storage interface.
type DependencyQueryStore interface {
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
	GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error)
	GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (blockedByMap map[string][]string, blocksMap map[string][]string, parentMap map[string]string, err error)
	IsBlocked(ctx context.Context, issueID string) (bool, []string, error)
	GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error)
	DetectCycles(ctx context.Context) ([][]*types.Issue, error)
	FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error)
	RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error
}
