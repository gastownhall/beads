// Package beads provides a minimal public API for extending bd with custom orchestration.
//
// Most extensions should use direct SQL queries against bd's database.
// This package exports only the essential types and functions needed for
// Go-based extensions that want to use bd's storage layer programmatically.
//
// For detailed guidance on extending bd, see docs/EXTENDING.md.
package beads

import (
	"context"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/types"
)

// Storage is the interface for beads storage operations
type Storage = beads.Storage

// Transaction provides atomic multi-operation support within a database transaction.
// Use Storage.RunInTransaction() to obtain a Transaction instance.
type Transaction = beads.Transaction

// NewSQLiteStorage creates a new SQLite storage instance at the given path.
// This function explicitly uses SQLite regardless of configuration.
//
// Note: This bypasses backend configuration. If your .beads directory is
// configured to use Dolt, this will still open SQLite (and likely fail or
// access wrong data). For most use cases, callers should use storage/factory
// package to respect backend configuration.
func NewSQLiteStorage(ctx context.Context, dbPath string) (Storage, error) {
	return beads.NewSQLiteStorage(ctx, dbPath)
}

// GetConfiguredBackend returns the backend type from the beads directory config.
// Returns "sqlite" if no config exists or backend is not specified.
func GetConfiguredBackend(beadsDir string) string {
	return beads.GetConfiguredBackend(beadsDir)
}

// FindDatabasePath finds the beads database in the current directory tree
func FindDatabasePath() string {
	return beads.FindDatabasePath()
}

// FindBeadsDir finds the .beads/ directory in the current directory tree
// Returns empty string if not found. Supports both database and JSONL-only mode.
func FindBeadsDir() string {
	return beads.FindBeadsDir()
}

// FindJSONLPath finds the JSONL file corresponding to a database path
func FindJSONLPath(dbPath string) string {
	return beads.FindJSONLPath(dbPath)
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
	Issue            = types.Issue
	Status           = types.Status
	IssueType        = types.IssueType
	Dependency       = types.Dependency
	DependencyType   = types.DependencyType
	Label            = types.Label
	Comment          = types.Comment
	Event            = types.Event
	EventType        = types.EventType
	BlockedIssue     = types.BlockedIssue
	TreeNode         = types.TreeNode
	IssueFilter      = types.IssueFilter
	WorkFilter       = types.WorkFilter
	StaleFilter      = types.StaleFilter
	DependencyCounts = types.DependencyCounts
	IssueWithCounts  = types.IssueWithCounts
	SortPolicy       = types.SortPolicy
	EpicStatus       = types.EpicStatus
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
