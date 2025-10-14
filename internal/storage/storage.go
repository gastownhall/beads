// Package storage defines the interface for issue storage backends.
package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// Storage defines the interface for issue storage backends
type Storage interface {
	// Issues
	CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
	CloseIssue(ctx context.Context, id string, reason string, actor string) error
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)

	// Dependencies
	AddDependency(ctx context.Context, dep *types.Dependency, actor string) error
	RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error
	GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error)
	GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error)
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
	GetDependencyTree(ctx context.Context, issueID string, maxDepth int) ([]*types.TreeNode, error)
	DetectCycles(ctx context.Context) ([][]*types.Issue, error)

	// Labels
	AddLabel(ctx context.Context, issueID, label, actor string) error
	RemoveLabel(ctx context.Context, issueID, label, actor string) error
	GetLabels(ctx context.Context, issueID string) ([]string, error)
	GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error)

	// Ready Work & Blocking
	GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error)
	GetBlockedIssues(ctx context.Context) ([]*types.BlockedIssue, error)

	// Events
	AddComment(ctx context.Context, issueID, actor, comment string) error
	GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error)

	// Statistics
	GetStatistics(ctx context.Context) (*types.Statistics, error)

	// Dirty tracking (for incremental JSONL export)
	GetDirtyIssues(ctx context.Context) ([]string, error)
	ClearDirtyIssues(ctx context.Context) error // WARNING: Race condition (bd-52), use ClearDirtyIssuesByID
	ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error

	// Config
	SetConfig(ctx context.Context, key, value string) error
	GetConfig(ctx context.Context, key string) (string, error)

	// Metadata (for internal state like import hashes)
	SetMetadata(ctx context.Context, key, value string) error
	GetMetadata(ctx context.Context, key string) (string, error)

	// Lifecycle
	Close() error
}

// Config holds database configuration
type Config struct {
	Backend string // "sqlite" or "postgres"

	// SQLite config
	Path string // database file path

	// PostgreSQL config
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string
}
