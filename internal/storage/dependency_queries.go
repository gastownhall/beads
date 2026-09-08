package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
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

	// IterAllDependencyRecords streams every dependency edge in the rig as
	// a flat sequence of *types.Dependency rows. Callers that today walk
	// GetAllDependencyRecords (which returns map[string][]*types.Dependency)
	// can rebuild that map by streaming and grouping on Dependency.IssueID.
	IterAllDependencyRecords(ctx context.Context) (Iter[types.Dependency], error)

	// CountDependentsByStatus returns the number of issues that depend on issueID
	// and are in the given status. Preferred over CountDependents + per-row filtering
	// for the bd close epic-closure check.
	CountDependentsByStatus(ctx context.Context, issueID string, status types.Status) (int64, error)
}
