// Package storage provides shared types for issue storage.
//
// The concrete storage implementation lives in the dolt sub-package.
// This package holds interface and value types that are referenced by
// both the dolt implementation and its consumers (cmd/bd, etc.).
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

// ErrNotFound is returned when a requested entity does not exist in the database.
var ErrNotFound = errors.New("not found")

// ErrNotInitialized is returned when the database has not been initialized
// (e.g., issue_prefix config is missing).
var ErrNotInitialized = errors.New("database not initialized")

// ErrPrefixMismatch is returned when an issue ID does not match the configured prefix.
var ErrPrefixMismatch = errors.New("prefix mismatch")

// Storage is the interface satisfied by *dolt.DoltStore.
// Consumers depend on this interface rather than on the concrete type so that
// alternative implementations (mocks, proxies, etc.) can be substituted.
type Storage interface {
	// Issue CRUD
	CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
	CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error)
	GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
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
	GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error)

	// Labels
	AddLabel(ctx context.Context, issueID, label, actor string) error
	RemoveLabel(ctx context.Context, issueID, label, actor string) error
	GetLabels(ctx context.Context, issueID string) ([]string, error)
	GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error)

	// Work queries
	GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error)
	GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error)
	GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error)

	// Comments and events
	AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error)
	GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error)
	GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error)
	GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error)

	// Statistics
	GetStatistics(ctx context.Context) (*types.Statistics, error)

	// Configuration
	SetConfig(ctx context.Context, key, value string) error
	GetConfig(ctx context.Context, key string) (string, error)
	GetAllConfig(ctx context.Context) (map[string]string, error)

	// Transactions
	RunInTransaction(ctx context.Context, commitMsg string, fn func(tx Transaction) error) error

	// Lifecycle
	Close() error
}

// CommentRefStore is an optional interface for stores that support comment external_ref operations.
// Used by the sync engine for bidirectional comment synchronization.
type CommentRefStore interface {
	GetCommentByExternalRef(ctx context.Context, issueID, externalRef string) (*types.Comment, error)
	ImportCommentWithRef(ctx context.Context, issueID, author, text, externalRef string, createdAt time.Time) (*types.Comment, error)
	UpdateCommentExternalRef(ctx context.Context, issueID, commentID, externalRef string) error
}

// AttachmentStore is an optional interface for stores that support attachment operations.
// Used by the sync engine for pulling attachment metadata from external trackers.
type AttachmentStore interface {
	CreateAttachment(ctx context.Context, att *types.Attachment) (*types.Attachment, error)
	GetIssueAttachments(ctx context.Context, issueID string) ([]*types.Attachment, error)
	GetAttachmentByExternalRef(ctx context.Context, issueID, externalRef string) (*types.Attachment, error)
}

// DoltStorage is the full interface for Dolt-backed stores, composing the core
// Storage interface with all capability sub-interfaces. Both DoltStore and
// EmbeddedDoltStore satisfy this interface.
type DoltStorage interface {
	Storage
	VersionControl
	HistoryViewer
	RemoteStore
	SyncStore
	FederationStore
	BulkIssueStore
	DependencyQueryStore
	AnnotationStore
	ConfigMetadataStore
	CompactionStore
	AdvancedQueryStore
}

// RawDBAccessor provides raw *sql.DB access for diagnostics and migrations.
// Callers that need raw SQL should type-assert to this interface.
type RawDBAccessor interface {
	DB() *sql.DB
	UnderlyingDB() *sql.DB
}

// StoreLocator provides filesystem path information for the store.
// Callers that need the store's on-disk location should type-assert to this interface.
type StoreLocator interface {
	Path() string
	CLIDir() string
}

// LifecycleManager provides lifecycle inspection beyond Close().
type LifecycleManager interface {
	IsClosed() bool
}

// PendingCommitter provides the ability to commit pending (dirty) changes.
// Used by auto-commit and auto-push flows.
type PendingCommitter interface {
	CommitPending(ctx context.Context, actor string) (bool, error)
}

// BackupStore provides Dolt backup operations (CALL DOLT_BACKUP) and
// JSONL table export/restore for disaster recovery.
// Callers that need backup functionality should type-assert to this interface.
type BackupStore interface {
	BackupAdd(ctx context.Context, name, url string) error
	BackupSync(ctx context.Context, name string) error
	BackupRemove(ctx context.Context, name string) error
	// BackupExportTables exports issues, events, comments, dependencies,
	// labels, and config to JSONL files in dir. When prefix is non-empty,
	// only issues matching "prefix-%" are exported.
	BackupExportTables(ctx context.Context, dir, prefix string) (*BackupCounts, error)
	// BackupRestoreFromDir restores all JSONL tables from dir. When prefix
	// is non-empty, only entries matching the prefix are imported. Config is
	// restored via the store's SetConfig; all other tables use raw INSERT IGNORE.
	BackupRestoreFromDir(ctx context.Context, dir, prefix string, dryRun bool) (*BackupRestoreResult, error)
}

// BackupCounts tracks the number of rows exported per table.
type BackupCounts struct {
	Issues       int
	Events       int
	Comments     int
	Dependencies int
	Labels       int
	Config       int
}

// BackupRestoreResult tracks what a restore operation did.
type BackupRestoreResult struct {
	Issues       int
	Comments     int
	Dependencies int
	Labels       int
	Events       int
	Config       int
	Warnings     int
}

// Transaction provides atomic multi-operation support within a single database transaction.
//
// The Transaction interface exposes a subset of storage methods that execute within
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
// # Example Usage
//
//	err := store.RunInTransaction(ctx, "bd: create parent and child", func(tx storage.Transaction) error {
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
