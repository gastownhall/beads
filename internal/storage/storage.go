// Package storage defines the interface for issue storage backends.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// ErrAlreadyClaimed is returned when attempting to claim an issue that is already
// claimed by another user. The error message contains the current assignee.
var ErrAlreadyClaimed = errors.New("issue already claimed")

// Transaction provides atomic multi-operation support within a single database transaction.
//
// The Transaction interface exposes a subset of Storage methods that execute within
// a single database transaction. This enables atomic workflows where multiple operations
// must either all succeed or all fail (e.g., creating issues with dependencies and labels).
//
// # Transaction Semantics
//
//   - All operations within the transaction share the same database connection
//   - Changes are not visible to other connections until commit
//   - If any operation returns an error, the transaction is rolled back
//   - If the callback function panics, the transaction is rolled back
//   - On successful return from the callback, the transaction is committed
//
// # SQLite Specifics
//
//   - Uses BEGIN IMMEDIATE mode to acquire write lock early
//   - This prevents deadlocks when multiple operations compete for the same lock
//   - IMMEDIATE mode serializes concurrent transactions properly
//
// # Example Usage
//
//	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
//	    // Create parent issue
//	    if err := tx.CreateIssue(ctx, parentIssue, actor); err != nil {
//	        return err // Triggers rollback
//	    }
//	    // Create child issue
//	    if err := tx.CreateIssue(ctx, childIssue, actor); err != nil {
//	        return err // Triggers rollback
//	    }
//	    // Add dependency between them
//	    if err := tx.AddDependency(ctx, dep, actor); err != nil {
//	        return err // Triggers rollback
//	    }
//	    return nil // Triggers commit
//	})
type Transaction interface {
	// Issue operations
	CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
	CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
	CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error
	DeleteIssue(ctx context.Context, id string) error
	GetIssue(ctx context.Context, id string) (*types.Issue, error)                                    // For read-your-writes within transaction
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) // For read-your-writes within transaction

	// Dependency operations
	AddDependency(ctx context.Context, dep *types.Dependency, actor string) error
	RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)

	// Label operations
	AddLabel(ctx context.Context, issueID, label, actor string) error
	RemoveLabel(ctx context.Context, issueID, label, actor string) error
	GetLabels(ctx context.Context, issueID string) ([]string, error)

	// Config operations (for atomic config + issue workflows)
	SetConfig(ctx context.Context, key, value string) error
	GetConfig(ctx context.Context, key string) (string, error)

	// Metadata operations (for internal state like import hashes)
	SetMetadata(ctx context.Context, key, value string) error
	GetMetadata(ctx context.Context, key string) (string, error)

	// Comment operations
	AddComment(ctx context.Context, issueID, actor, comment string) error
	ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error)
	GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error)
}

// Storage defines the interface for issue storage backends
type Storage interface {
	// Issues
	CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
	CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error
	CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts BatchCreateOptions) error
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
	// ClaimIssue atomically claims an issue using compare-and-swap semantics.
	// It sets the assignee to actor and status to "in_progress" only if the issue
	// has no current assignee. Returns ErrAlreadyClaimed if the issue is already
	// claimed by another user. This provides race-condition-free claiming for
	// concurrent agents.
	ClaimIssue(ctx context.Context, id string, actor string) error
	CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error
	DeleteIssue(ctx context.Context, id string) error
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)

	// Dependencies
	AddDependency(ctx context.Context, dep *types.Dependency, actor string) error
	RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error
	GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error)
	GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error)
	GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error)
	GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error)
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
	GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)
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
	GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error)
	IsBlocked(ctx context.Context, issueID string) (bool, []string, error) // GH#962: Check if issue has open blockers
	GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error)
	GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error)
	GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) // GH#679

	// Events
	AddComment(ctx context.Context, issueID, actor, comment string) error
	GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error)
	GetAllEventsSince(ctx context.Context, sinceID int64) ([]*types.Event, error)

	// Comments
	AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error)
	// ImportIssueComment adds a comment while preserving the original timestamp.
	// Used during JSONL import to avoid timestamp drift across sync cycles.
	ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error)
	GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error)
	GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error)
	GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error)

	// Statistics
	GetStatistics(ctx context.Context) (*types.Statistics, error)

	// Molecule progress (efficient for large molecules)
	GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error)

	// Dirty tracking (for incremental JSONL export)
	GetDirtyIssues(ctx context.Context) ([]string, error)
	GetDirtyIssueHash(ctx context.Context, issueID string) (string, error) // For timestamp-only dedup (bd-164)
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
	GetCustomStatuses(ctx context.Context) ([]string, error) // Custom status states from status.custom config
	GetCustomTypes(ctx context.Context) ([]string, error)    // Custom issue types from types.custom config

	// Metadata (for internal state like import hashes)
	SetMetadata(ctx context.Context, key, value string) error
	GetMetadata(ctx context.Context, key string) (string, error)

	// Prefix rename operations
	UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error
	RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error
	RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error

	// Transactions
	//
	// RunInTransaction executes a function within a database transaction.
	// The Transaction interface provides atomic multi-operation support.
	//
	// Transaction behavior:
	//   - If fn returns nil, the transaction is committed
	//   - If fn returns an error, the transaction is rolled back
	//   - If fn panics, the transaction is rolled back and the panic is re-raised
	//   - Uses BEGIN IMMEDIATE for SQLite to acquire write lock early
	//
	// Example:
	//   err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
	//       if err := tx.CreateIssue(ctx, issue, actor); err != nil {
	//           return err // Triggers rollback
	//       }
	//       return nil // Triggers commit
	//   })
	RunInTransaction(ctx context.Context, fn func(tx Transaction) error) error

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

// CompactableStorage extends Storage with compaction capabilities.
// Not all storage backends support compaction (e.g., Dolt does not).
// RPC handlers should type-assert to this interface when compaction is requested.
// This interface is compatible with compact.Store for use with the compaction subsystem.
type CompactableStorage interface {
	Storage

	// CheckEligibility determines if an issue can be compacted at the given tier.
	// Returns (eligible, reason, error) where reason explains ineligibility.
	CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error)

	// GetTier1Candidates returns issues eligible for Tier 1 (basic) compaction.
	// Tier 1: closed 30+ days, no open dependents, not already compacted.
	GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error)

	// GetTier2Candidates returns issues eligible for Tier 2 (aggressive) compaction.
	// Tier 2: closed 90+ days, already Tier 1 compacted, meets stricter criteria.
	GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error)

	// ApplyCompaction updates the compaction metadata for an issue after compaction.
	// Sets compaction_level, compacted_at, compacted_at_commit, and original_size fields.
	ApplyCompaction(ctx context.Context, issueID string, level int, originalSize int, compressedSize int, commitHash string) error

	// MarkIssueDirty marks an issue as needing export to JSONL.
	MarkIssueDirty(ctx context.Context, issueID string) error
}

// BatchDeleter extends Storage with batch delete capabilities.
// Supports cascade deletion and dry-run mode for safe bulk operations.
type BatchDeleter interface {
	Storage

	// DeleteIssues deletes multiple issues in a single transaction.
	// If cascade is true, recursively deletes dependents.
	// If cascade is false but force is true, deletes issues and orphans dependents.
	// If both are false, returns an error if any issue has dependents.
	// If dryRun is true, only computes statistics without deleting.
	DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error)
}
