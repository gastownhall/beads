// Package beads provides a minimal public API for extending bd with custom orchestration.
//
// Most extensions should use direct SQL queries against bd's database.
// This package exports only the essential types and functions needed for
// Go-based extensions that want to use bd's storage layer programmatically.
//
// For a working extension example, see examples/bd-example-extension-go.
package beads

import (
	"context"

	"github.com/steveyegge/beads/internal/backend"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// Storage is the interface for beads storage operations
type Storage = beads.Storage

// Transaction provides atomic multi-operation support within a database transaction.
// Use Storage.RunInTransaction() to obtain a Transaction instance.
type Transaction = beads.Transaction

// BackendCapabilities describes optional backend behavior without exposing
// concrete database drivers or connection handles.
type BackendCapabilities struct {
	Embedded          bool
	Transactions      bool
	RawSQL            bool
	Leases            bool
	Maintenance       bool
	Versioning        bool
	Branching         bool
	DoltRemotes       bool
	ConcurrentWriters bool
}

// BackendInfo identifies the backend selected by OpenConfigured.
type BackendInfo struct {
	Name         string
	External     bool
	Capabilities BackendCapabilities
}

// OpenConfiguredOptions controls behavior that cannot be inferred from the
// workspace's metadata.json.
type OpenConfiguredOptions struct {
	ReadOnly bool
}

// RemoteStore provides dolt remote management and replication operations.
// Use type assertion on a Storage value to access these methods:
//
//	if rs, ok := store.(beads.RemoteStore); ok {
//	    rs.Push(ctx)
//	}
type RemoteStore = storage.RemoteStore

// SyncStore provides high-level sync operations with peers.
type SyncStore = storage.SyncStore

// VersionControlReader provides read-only version control operations.
// Write operations (Branch, Checkout, Merge, DeleteBranch) are not yet
// part of the public API. If you need them, please open an issue.
type VersionControlReader interface {
	CurrentBranch(ctx context.Context) (string, error)
	ListBranches(ctx context.Context) ([]string, error)
	CommitExists(ctx context.Context, commitHash string) (bool, error)
	GetCurrentCommit(ctx context.Context) (string, error)
	Status(ctx context.Context) (*VCStatus, error)
	Log(ctx context.Context, limit int) ([]CommitInfo, error)
}

// Replication and version control types from internal/storage
type (
	RemoteInfo  = storage.RemoteInfo
	SyncResult  = storage.SyncResult
	SyncStatus  = storage.SyncStatus
	Conflict    = storage.Conflict
	CommitInfo  = storage.CommitInfo
	VCStatus    = storage.Status
	StatusEntry = storage.StatusEntry
)

// Open opens a Dolt-backed beads database at the given path.
// This always opens in embedded mode. Use OpenFromConfig to respect
// server mode settings from metadata.json.
func Open(ctx context.Context, dbPath string) (Storage, error) {
	return dolt.New(ctx, &dolt.Config{Path: dbPath, CreateIfMissing: true})
}

// OpenConfigured opens the built-in or trusted external backend selected by
// metadata.json and returns its backend-neutral descriptor. External plugin
// commands are resolved from local/user trust configuration or environment;
// committed metadata cannot authorize executable code.
func OpenConfigured(ctx context.Context, beadsDir string, opts OpenConfiguredOptions) (Storage, BackendInfo, error) {
	store, descriptor, err := backend.OpenConfigured(ctx, beadsDir, backend.ConfiguredOpenOptions{ReadOnly: opts.ReadOnly})
	if err != nil {
		return nil, BackendInfo{}, err
	}
	return store, backendInfoFromInternal(descriptor), nil
}

// OpenFromConfig opens the backend selected by metadata.json. It is retained
// for source compatibility; new callers that need backend information should
// use OpenConfigured.
func OpenFromConfig(ctx context.Context, beadsDir string) (Storage, error) {
	store, _, err := OpenConfigured(ctx, beadsDir, OpenConfiguredOptions{})
	return store, err
}

func backendInfoFromInternal(in backend.Descriptor) BackendInfo {
	return BackendInfo{
		Name:     in.Name,
		External: in.External,
		Capabilities: BackendCapabilities{
			Embedded:          in.Capabilities.Embedded,
			Transactions:      in.Capabilities.Transactions,
			RawSQL:            in.Capabilities.RawSQL,
			Leases:            in.Capabilities.Leases,
			Maintenance:       in.Capabilities.Maintenance,
			Versioning:        in.Capabilities.Versioning,
			Branching:         in.Capabilities.Branching,
			DoltRemotes:       in.Capabilities.DoltRemotes,
			ConcurrentWriters: in.Capabilities.ConcurrentWriters,
		},
	}
}

// FindDatabasePath finds the beads database in the current directory tree
func FindDatabasePath() string {
	return beads.FindDatabasePath()
}

// FindBeadsDir finds the .beads/ directory in the current directory tree.
// Returns empty string if not found.
func FindBeadsDir() string {
	return beads.FindBeadsDir()
}

// DatabaseInfo contains information about a beads database
type DatabaseInfo = beads.DatabaseInfo

// FindAllDatabases finds all beads databases in the system
func FindAllDatabases() []DatabaseInfo {
	return beads.FindAllDatabases()
}

// RedirectInfo contains information about a beads directory redirect
type RedirectInfo = beads.RedirectInfo

// GetRedirectInfo checks if the current beads directory is redirected.
// Returns RedirectInfo with IsRedirected=true if a redirect is active.
func GetRedirectInfo() RedirectInfo {
	return beads.GetRedirectInfo()
}

// Core types from internal/types
type (
	Issue                       = types.Issue
	Status                      = types.Status
	IssueType                   = types.IssueType
	Dependency                  = types.Dependency
	DependencyType              = types.DependencyType
	Label                       = types.Label
	Comment                     = types.Comment
	Event                       = types.Event
	EventType                   = types.EventType
	BlockedIssue                = types.BlockedIssue
	TreeNode                    = types.TreeNode
	IssueFilter                 = types.IssueFilter
	WorkFilter                  = types.WorkFilter
	StaleFilter                 = types.StaleFilter
	DependencyCounts            = types.DependencyCounts
	IssueWithCounts             = types.IssueWithCounts
	IssueWithDependencyMetadata = types.IssueWithDependencyMetadata
	SortPolicy                  = types.SortPolicy
	EpicStatus                  = types.EpicStatus
	WispFilter                  = types.WispFilter
)

// Status constants
const (
	StatusOpen       = types.StatusOpen
	StatusInProgress = types.StatusInProgress
	StatusBlocked    = types.StatusBlocked
	StatusDeferred   = types.StatusDeferred
	StatusClosed     = types.StatusClosed
)

// IssueType constants
const (
	TypeBug     = types.TypeBug
	TypeFeature = types.TypeFeature
	TypeTask    = types.TypeTask
	TypeEpic    = types.TypeEpic
	TypeChore   = types.TypeChore
)

// DependencyType constants
const (
	DepBlocks            = types.DepBlocks
	DepRelated           = types.DepRelated
	DepParentChild       = types.DepParentChild
	DepDiscoveredFrom    = types.DepDiscoveredFrom
	DepConditionalBlocks = types.DepConditionalBlocks // B runs only if A fails (bd-kzda)
)

// SortPolicy constants
const (
	SortPolicyHybrid   = types.SortPolicyHybrid
	SortPolicyPriority = types.SortPolicyPriority
	SortPolicyOldest   = types.SortPolicyOldest
)

// EventType constants
const (
	EventCreated           = types.EventCreated
	EventUpdated           = types.EventUpdated
	EventStatusChanged     = types.EventStatusChanged
	EventCommented         = types.EventCommented
	EventClosed            = types.EventClosed
	EventReopened          = types.EventReopened
	EventDependencyAdded   = types.EventDependencyAdded
	EventDependencyRemoved = types.EventDependencyRemoved
	EventLabelAdded        = types.EventLabelAdded
	EventLabelRemoved      = types.EventLabelRemoved
	EventCompacted         = types.EventCompacted
)
