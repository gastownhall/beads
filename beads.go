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

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// Storage is the interface for beads storage operations
type Storage = beads.Storage

func configuredBackendUnavailable(backend string) error {
	switch backend {
	case configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite:
		return configfile.RemovedBackendError(backend)
	default:
		return configfile.UnknownBackendError(backend)
	}
}

// Transaction provides atomic multi-operation support within a database transaction.
// Use Storage.RunInTransaction() to obtain a Transaction instance.
type Transaction = beads.Transaction

// DependencyAddOptions controls transaction-scoped dependency insertion for
// Transaction.AddDependencyWithOptions. Exported so embedders' bulk graph
// writers can set SkipCycleCheck per edge and run one whole-graph
// Transaction.CycleThroughEdges pass before commit (bd-6dnrw.8) instead of
// paying the recursive per-edge cycle query — which cannot finish inside a
// per-command budget on molecule-sized graphs (observed: a 67-node/100-edge
// batch blowing a 120s deadline mid-transaction, gascity 2026-07-17).
//
// Callers that set SkipCycleCheck MUST run Transaction.CycleThroughEdges before
// commit and fail on new blocks/conditional-blocks/parent-child cycles
// (waits-for is excluded); skipping the per-edge check trades per-edge cost for
// one whole-graph check, never graph integrity.
type DependencyAddOptions = storage.DependencyAddOptions

// DependencyRemoveOptions controls transaction-scoped dependency removal for
// Transaction.RemoveDependencyWithOptions. Exported so embedders can request the
// dependency_removed history event on an explicit edge removal; the plain
// RemoveDependency default stays silent for structural edge teardown, mirroring
// the DependencyAddOptions/AddDependencyWithOptions split.
type DependencyRemoveOptions = storage.DependencyRemoveOptions

// CloseIssueOptions carries the optional inputs to Storage.CloseIssueChecked —
// an atomic, guarded close that refuses a still-blocked issue with
// ErrCloseBlocked unless Force is set. Exported so consumers can name it without
// importing internal/storage.
type CloseIssueOptions = storage.CloseIssueOptions

// CloseIssueResult reports the outcome of Storage.CloseIssueChecked. Unchanged
// is true when the issue was already closed (idempotent no-op).
type CloseIssueResult = storage.CloseIssueResult

// UpdateIssueOptions carries the optional inputs to Storage.UpdateIssueChecked —
// an update with an optional ExpectedVersion compare-and-swap that refuses a
// concurrently-modified issue with ErrVersionMismatch. Exported so consumers can
// name it without importing internal/storage.
type UpdateIssueOptions = storage.UpdateIssueOptions

// CommentPageCursor is the resume position for Storage.GetIssueCommentsPage — the
// (created_at, id) of the last comment already returned, with the zero value
// starting a walk from the beginning of the thread. Exported so consumers can
// name it without importing internal/storage.
type CommentPageCursor = storage.CommentPageCursor

// RemoteStore provides dolt remote management and replication operations.
// Use type assertion on a Storage value to access these methods:
//
//	if rs, ok := store.(beads.RemoteStore); ok {
//	    rs.Push(ctx)
//	}
type RemoteStore = storage.RemoteStore

// SyncStore provides high-level sync operations with peers.
type SyncStore = storage.SyncStore

// EventCursor is a keyset position in the durable events stream, ordered by
// (created_at, id). The zero value means "from the beginning".
type EventCursor = storage.EventCursor

// EventQuerier is the durable-event-feed surface of a Storage: keyset paging
// over the durable event log, beyond the base Storage's time-only
// GetAllEventsSince. It is a NARROW, hand-declared root interface exposing
// exactly what consumers need — not an alias of the internal EventQueryStore —
// so the published surface stays small and independent of the engine interface.
// Reach it via AsEventQuerier.
type EventQuerier interface {
	// EventsSince returns durable events strictly after cursor, ordered by
	// (created_at ASC, id ASC), bounded by limit (0 = a store default, capped).
	// issueID scopes the feed to one bead's history ("" = all).
	EventsSince(ctx context.Context, cursor EventCursor, issueID string, limit int) ([]*Event, error)
}

// DependentQuerier is the target-keyed dependents surface of a Storage: the raw
// inbound-edge reads that back group-membership. Like EventQuerier it is a
// NARROW hand-declared root interface (not an alias of DependencyQueryStore),
// exposing only the calls consumers use. Reach it via AsDependentQuerier.
type DependentQuerier interface {
	// GetDependentRecords returns raw dependency rows whose target is targetID,
	// paged by the dependency row id (afterID, "" = start). See the engine doc
	// for the two-table span and raw-read/policy-at-hydration contract.
	GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*Dependency, error)
	// GetDependentRecordsForIssues returns raw dependency rows keyed by TARGET id
	// — for a SET of target ids in one batched read, each id's inbound edges (its
	// dependents), across both dependency tables, ALL dep types, de-duped by row
	// id. The batched, target-keyed mirror of GetDependencyRecordsForIssues; same
	// two-table span and raw-read/policy-at-hydration contract as
	// GetDependentRecords, without paging.
	GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*Dependency, error)
	// CountDependentRecords returns the true total inbound-edge count of targetID
	// (depType "" = all) without paging.
	CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error)
}

// BlockedQuerier is the transitive-blocked surface of a Storage: the
// denormalized is_blocked flag, single or batched. Like EventQuerier it is a
// NARROW hand-declared root interface (not an alias of DependencyQueryStore),
// exposing only the reads consumers use. IsBlockedBatch returns the is_blocked
// column for a whole page in one round-trip — the same transitive value
// IsBlocked returns per id, with no N-call fan-out. Reach it via AsBlockedQuerier.
type BlockedQuerier interface {
	// IsBlocked reports whether issueID is blocked (its denormalized transitive
	// is_blocked flag) and the open direct blockers for display.
	IsBlocked(ctx context.Context, issueID string) (bool, []string, error)
	// IsBlockedBatch returns the denormalized transitive is_blocked flag for each
	// of ids in one batched read. ids present in neither the issues nor wisps
	// table are absent from the map; callers treat absent as not-blocked.
	IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error)
}

// AsEventQuerier returns the EventQuerier view of s, or (nil, false) when the
// backing store does not expose the durable-event feed. Assert once and fail
// loud. A single direct assertion is sufficient — see the decorator contract on
// AsIssueClaimer.
func AsEventQuerier(s Storage) (EventQuerier, bool) {
	q, ok := s.(EventQuerier)
	return q, ok
}

// AsBlockedQuerier returns the BlockedQuerier view of s, or (nil, false) when the
// backing store does not expose the transitive-blocked reads. Assert once and
// fail loud. A single direct assertion is sufficient — see the decorator
// contract on AsIssueClaimer.
func AsBlockedQuerier(s Storage) (BlockedQuerier, bool) {
	q, ok := s.(BlockedQuerier)
	return q, ok
}

// AsDependentQuerier returns the DependentQuerier view of s, or (nil, false) when
// the backing store does not expose the target-keyed dependents reads. Assert
// once and fail loud. A single direct assertion is sufficient — see the
// decorator contract on AsIssueClaimer.
func AsDependentQuerier(s Storage) (DependentQuerier, bool) {
	q, ok := s.(DependentQuerier)
	return q, ok
}

// ErrCircuitOpen is re-exported (aliased, so errors.Is works across the package
// boundary) from the Dolt storage layer: a read or write rejected because the
// Dolt circuit breaker is open wraps it. The claim sentinels ErrAlreadyClaimed
// and ErrNotClaimable — the ones ParseClaimConflict recovers assignee/status
// detail from — are re-exported with the other error sentinels below.
var (
	ErrCircuitOpen = dolt.ErrCircuitOpen
)

// IssueClaimer is the atomic-claim surface of a Storage. ClaimIssue and
// ClaimReadyIssue live on the storage.BulkIssueStore extension rather than the
// base Storage interface, so callers reach them by type-assertion via
// AsIssueClaimer rather than off the Storage value directly.
type IssueClaimer interface {
	// ClaimIssue atomically claims id for actor using compare-and-swap
	// semantics (open ∧ unassigned-or-same-actor). Returns a wrapped
	// ErrAlreadyClaimed or ErrNotClaimable on conflict.
	ClaimIssue(ctx context.Context, id string, actor string) error
	// ClaimReadyIssue atomically claims the first ready issue matching filter,
	// or returns (nil, nil) when none is claimable.
	ClaimReadyIssue(ctx context.Context, filter WorkFilter, actor string) (*Issue, error)
}

// AsIssueClaimer returns the IssueClaimer view of s when the backing store
// supports atomic claim (Dolt-backed stores do), and (nil, false) otherwise.
// Assert once at startup and fail loud.
//
// DECORATOR CONTRACT: a single direct type-assertion is sufficient — no unwrap.
// ClaimIssue/ClaimReadyIssue (like EventsSince and the dependents reads) live on
// the engine interface storage.DoltStorage, and the compile-time drift guards
// below prove storage.DoltStorage satisfies each narrow surface. A store
// decorator therefore MUST embed storage.DoltStorage — as HookFiringStore does —
// which promotes these methods so the assertion reaches them THROUGH the
// decorator. (This is unlike the cmd/bd optional interfaces — StoreLocator,
// BackupStore, Flattener, … — which are NOT part of DoltStorage, do not promote,
// and so genuinely need storage.UnwrapStore.) A former storage.UnwrapStore
// fallback here was provably dead: whenever s satisfies storage.DoltStorage it
// already satisfies the narrow surface (drift guard), so the direct assertion
// always wins; and the only decorator, HookFiringStore, forwards by promotion.
func AsIssueClaimer(s Storage) (IssueClaimer, bool) {
	c, ok := s.(IssueClaimer)
	return c, ok
}

// Compile-time drift guards: the full engine interface storage.DoltStorage must
// satisfy each narrow public surface, so a signature change to the claim / event
// / dependents methods on the engine breaks the build here instead of silently
// making As* return false at runtime. Concrete-store conformance is asserted in
// the tests (both Dolt stores).
var (
	_ IssueClaimer     = (storage.DoltStorage)(nil)
	_ EventQuerier     = (storage.DoltStorage)(nil)
	_ DependentQuerier = (storage.DoltStorage)(nil)
	_ BlockedQuerier   = (storage.DoltStorage)(nil)
)

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

// OpenFromConfig opens the Dolt implementation using configuration from
// metadata.json. Unlike Open, this respects Dolt server mode settings and database
// name configuration.
// beadsDir is the path to the .beads directory.
func OpenFromConfig(ctx context.Context, beadsDir string) (Storage, error) {
	return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
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
	EventClaimed           = types.EventClaimed
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

// MaxFieldLen re-exports the maximum length (in characters) of the assignee,
// owner, and label fields, paired with the ErrFieldTooLong sentinel below.
const MaxFieldLen = types.MaxFieldLen

// Re-exported error sentinels so consumers match on errors.Is rather than
// on message text. Each is an ALIAS of the internal sentinel, so the identity
// is preserved across the package boundary.
var (
	ErrNotFound        = storage.ErrNotFound
	ErrAlreadyClaimed  = storage.ErrAlreadyClaimed
	ErrNotClaimable    = storage.ErrNotClaimable
	ErrCloseBlocked    = storage.ErrCloseBlocked
	ErrVersionMismatch = storage.ErrVersionMismatch
	ErrSelfDependency  = domain.ErrSelfDependency
	ErrDependencyCycle = domain.ErrDependencyCycle
	ErrFieldTooLong    = types.ErrFieldTooLong
)

// DependencyTypeConflictError is returned by AddDependency when an edge of a
// different type already exists between the pair; callers errors.As it to read
// the existing/requested types instead of parsing the message.
type DependencyTypeConflictError = domain.DependencyTypeConflictError

// DependencyHierarchyConflictError is returned by AddDependency when a blocking
// edge would gate an issue on its own ancestor/descendant (a gate that can
// never clear).
type DependencyHierarchyConflictError = domain.DependencyHierarchyConflictError
