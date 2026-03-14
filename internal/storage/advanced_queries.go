package storage

import (
	"context"

	"github.com/gastownhall/beads/internal/types"
)

// AdvancedQueryStore provides repo mtime tracking, molecule queries, and stale issue detection.
type AdvancedQueryStore interface {
	GetRepoMtime(ctx context.Context, repoPath string) (int64, error)
	SetRepoMtime(ctx context.Context, repoPath, jsonlPath string, mtimeNs int64) error
	ClearRepoMtime(ctx context.Context, repoPath string) error
	GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error)
	GetMoleculeLastActivity(ctx context.Context, moleculeID string) (*types.MoleculeLastActivity, error)
	GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error)
}
