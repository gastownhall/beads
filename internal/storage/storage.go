// Package storage defines the interface for issue storage backends.
package storage

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/types"
)

// Storage defines the interface for issue storage backends
type Storage interface {
	// Issues
	CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
	CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
	CloseIssue(ctx context.Context, id string, reason string, actor string) error
	DeleteIssue(ctx context.Context, id string) error
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)

	// Dependencies
	AddDependency(ctx context.Context, dep *types.Dependency, actor string) error
	RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error
	GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error)
	GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error)
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
	GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error)
	GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error)
	DetectCycles(ctx context.Context) ([][]*types.Issue, error)

	// Labels
	AddLabel(ctx context.Context, issueID, label, actor string) error
	RemoveLabel(ctx context.Context, issueID, label, actor string) error
	GetLabels(ctx context.Context, issueID string) ([]string, error)
	GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error)
	GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error)

	// Ready Work & Blocking
	GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error)
	GetBlockedIssues(ctx context.Context) ([]*types.BlockedIssue, error)
	GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error)
	GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error)

	// Events
	AddComment(ctx context.Context, issueID, actor, comment string) error
	GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error)

	// Comments
	AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error)
	GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error)

	// Statistics
	GetStatistics(ctx context.Context) (*types.Statistics, error)

	// Dirty tracking (for incremental JSONL export)
	GetDirtyIssues(ctx context.Context) ([]string, error)
	GetDirtyIssueHash(ctx context.Context, issueID string) (string, error) // For timestamp-only dedup (bd-164)
	ClearDirtyIssues(ctx context.Context) error                            // WARNING: Race condition (bd-52), use ClearDirtyIssuesByID
	ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error

	// Export hash tracking (for timestamp-only dedup, bd-164)
	GetExportHash(ctx context.Context, issueID string) (string, error)
	SetExportHash(ctx context.Context, issueID, contentHash string) error
	ClearAllExportHashes(ctx context.Context) error
	
	// JSONL file integrity (bd-160)
	GetJSONLFileHash(ctx context.Context) (string, error)
	SetJSONLFileHash(ctx context.Context, fileHash string) error

	// ID Generation
	GetNextChildID(ctx context.Context, parentID string) (string, error)

	// Config
	SetConfig(ctx context.Context, key, value string) error
	GetConfig(ctx context.Context, key string) (string, error)
	GetAllConfig(ctx context.Context) (map[string]string, error)
	DeleteConfig(ctx context.Context, key string) error

	// Metadata (for internal state like import hashes)
	SetMetadata(ctx context.Context, key, value string) error
	GetMetadata(ctx context.Context, key string) (string, error)

	// Prefix rename operations
	UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error
	RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error
	RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error

	// Lifecycle
	Close() error

	// Database path (for daemon validation)
	Path() string

	// UnderlyingDB returns the underlying *sql.DB connection
	// This is provided for extensions (like VC) that need to create their own tables
	// in the same database. Extensions should use foreign keys to reference core tables.
	// WARNING: Direct database access bypasses the storage layer. Use with caution.
	UnderlyingDB() *sql.DB

	// UnderlyingConn returns a single connection from the pool for scoped use.
	// Useful for migrations and DDL operations that benefit from explicit connection lifetime.
	// The caller MUST close the connection when done to return it to the pool.
	// For general queries, prefer UnderlyingDB() which manages the pool automatically.
	UnderlyingConn(ctx context.Context) (*sql.Conn, error)
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
