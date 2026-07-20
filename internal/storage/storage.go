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

// ErrNotClaimable is returned when attempting to claim an issue that is not in a
// claimable state, such as closed, deferred, or already in progress without the
// same actor owning the claim.
var ErrNotClaimable = errors.New("issue not claimable")

// ErrNotOwner is returned when an actor tries to unclaim an issue that is claimed
// by a different actor. Releasing another actor's claim requires the force
// escape hatch (bd unclaim --force), reserved for admin/reaper use.
var ErrNotOwner = errors.New("issue claimed by a different actor")

// ErrAssigneeMismatch is returned by UnclaimIssueIfAssignee when the issue's
// current assignee does not match the expected assignee (including when the
// issue is no longer assigned at all). The caller's view of the claim was
// stale; the issue is left untouched.
var ErrAssigneeMismatch = errors.New("assignee mismatch")

// ErrNotFound is returned when a requested entity does not exist in the database.
var ErrNotFound = errors.New("not found")

// ErrNotInitialized is returned when the database has not been initialized
// (e.g., issue_prefix config is missing).
var ErrNotInitialized = errors.New("database not initialized")

// ErrPrefixMismatch is returned when an issue ID does not match the configured prefix.
var ErrPrefixMismatch = errors.New("prefix mismatch")

// ErrCloseBlocked is returned by CloseIssueChecked when an issue cannot be
// closed because it is still blocked (is_blocked=1: an open blocking dependency
// or an open blocking gate). Bypass with CloseIssueOptions.Force.
var ErrCloseBlocked = errors.New("cannot close blocked issue")

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
	ReopenIssue(ctx context.Context, id string, reason string, actor string) error
	UnclaimIssue(ctx context.Context, id string, actor string, force bool) error
	// UnclaimIssueIfAssignee releases a claim only while the issue is still
	// assigned to expectedAssignee (compare-and-swap, the inverse of
	// ClaimIssue). Returns ErrAssigneeMismatch, leaving the issue untouched,
	// when the current assignee differs.
	UnclaimIssueIfAssignee(ctx context.Context, id string, actor string, expectedAssignee string) error
	UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error
	CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error
	// CloseIssueChecked closes an issue, but refuses with ErrCloseBlocked when
	// the issue is still blocked (is_blocked=1) unless opts.Force is set. The
	// blocked-check and the close run in ONE transaction, so the guard is atomic
	// (no TOCTOU). Already-closed is an idempotent success with Unchanged=true; a
	// missing issue returns ErrNotFound.
	CloseIssueChecked(ctx context.Context, id string, actor string, opts CloseIssueOptions) (CloseIssueResult, error)
	DeleteIssue(ctx context.Context, id string) error
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
	SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error)
	// SearchIssueIDs is a narrow-projection variant of SearchIssues that
	// returns only matching issue IDs. Use when full row hydration is wasted
	// (e.g., partial-ID resolution in internal/utils/id_parser.go).
	SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error)

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
	GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error)
	GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error)
	GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error)

	// Wisp queries
	// ListWisps returns ephemeral issues matching the filter.
	// It always restricts to Ephemeral=true; callers do not need to set that flag.
	ListWisps(ctx context.Context, filter types.WispFilter) ([]*types.Issue, error)

	// Comments and events
	AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error)
	GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error)
	GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error)
	GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error)

	// Aggregate counts — cheaper than materializing rows when only cardinality is needed.
	// Filter.Limit and Filter.Offset are ignored by CountIssues; all others apply.

	// CountIssues returns the number of issues matching query and filter.
	CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error)
	// CountIssuesByGroup returns per-group counts. groupBy is one of:
	// status, priority, type, assignee, label.
	CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error)
	// CountDependents returns the number of issues that depend on issueID.
	CountDependents(ctx context.Context, issueID string) (int64, error)
	// CountDependencies returns the number of issues that issueID depends on.
	CountDependencies(ctx context.Context, issueID string) (int64, error)
	// CountIssueComments returns the number of comments on an issue.
	CountIssueComments(ctx context.Context, issueID string) (int64, error)
	// CountEvents returns the number of audit events for an issue, capped at limit
	// (or unbounded if limit == 0).
	CountEvents(ctx context.Context, issueID string, limit int) (int64, error)

	// Streaming iterators (be-jaavsb / be-yinl4d).
	//
	// IterIssues streams issues matching the filter. Use this in place of
	// SearchIssues when the result set is potentially unbounded
	// (filter.Limit == 0 or absent). For bounded queries SearchIssues
	// remains the right call.
	IterIssues(ctx context.Context, query string, filter types.IssueFilter) (Iter[types.Issue], error)
	// IterDependentsWithMetadata streams dependents (issues that depend on
	// issueID) with the relationship metadata attached. Replaces the slice
	// path for bd show --json --include-dependents on hub beads.
	IterDependentsWithMetadata(ctx context.Context, issueID string) (Iter[types.IssueWithDependencyMetadata], error)
	// IterDependenciesWithMetadata is the inverse direction — issues that
	// issueID depends on, with metadata.
	IterDependenciesWithMetadata(ctx context.Context, issueID string) (Iter[types.IssueWithDependencyMetadata], error)
	// IterIssueComments streams comments on an issue, ordered by created_at.
	IterIssueComments(ctx context.Context, issueID string) (Iter[types.Comment], error)
	// IterEvents streams the audit-trail events for an issue, ordered by
	// created_at descending. limit==0 means unbounded.
	IterEvents(ctx context.Context, issueID string, limit int) (Iter[types.Event], error)
	// IterAllEventsSince streams every audit-trail event in the rig newer
	// than `since`. There is no bounded variant — full-rig event scans are
	// inherently unbounded.
	IterAllEventsSince(ctx context.Context, since time.Time) (Iter[types.Event], error)
	// IterReadyWork streams issues that are ready for work (no open
	// blockers), matching the filter.
	IterReadyWork(ctx context.Context, filter types.WorkFilter) (Iter[types.Issue], error)
	// IterBlockedIssues streams blocked issues (with the blockers surfaced
	// in BlockedIssue), matching the filter.
	IterBlockedIssues(ctx context.Context, filter types.WorkFilter) (Iter[types.BlockedIssue], error)
	// IterWisps streams ephemeral issues matching the filter. Always
	// restricts to Ephemeral=true; callers do not need to set that flag.
	IterWisps(ctx context.Context, filter types.WispFilter) (Iter[types.Issue], error)

	// Statistics
	GetStatistics(ctx context.Context) (*types.Statistics, error)

	// Configuration
	SetConfig(ctx context.Context, key, value string) error
	GetConfig(ctx context.Context, key string) (string, error)
	GetAllConfig(ctx context.Context) (map[string]string, error)

	// Local metadata operations (dolt-ignored, clone-local state).
	// Used for tip timestamps, version stamps, tracker sync cursors, etc.
	// Data is ephemeral — callers must handle ("", nil) as the normal case.
	SetLocalMetadata(ctx context.Context, key, value string) error
	GetLocalMetadata(ctx context.Context, key string) (string, error)

	// Transactions
	RunInTransaction(ctx context.Context, commitMsg string, fn func(tx Transaction) error) error

	// MergeSlot — serialized conflict resolution primitive.
	// Each rig has one merge slot bead (<prefix>-merge-slot, labeled gt:slot).
	// The slot ID is derived from the issue_prefix config key.
	MergeSlotCreate(ctx context.Context, actor string) (*types.Issue, error)
	MergeSlotCheck(ctx context.Context) (*MergeSlotStatus, error)
	MergeSlotAcquire(ctx context.Context, holder, actor string, wait bool) (*MergeSlotResult, error)
	MergeSlotRelease(ctx context.Context, holder, actor string) error

	// Metadata slots — key-value pairs stored in issue metadata JSON.
	// Used by gt for delegation tracking, hook state, and other per-issue data.
	SlotSet(ctx context.Context, issueID, key, value, actor string) error
	SlotGet(ctx context.Context, issueID, key string) (string, error)
	SlotClear(ctx context.Context, issueID, key, actor string) error

	// Lifecycle
	Close() error
}

// CloseIssueOptions carries the optional inputs to CloseIssueChecked.
type CloseIssueOptions struct {
	Reason  string
	Session string
	Force   bool // bypass the is_blocked guard (mirrors `bd close --force`)
}

// CloseIssueResult reports the outcome of CloseIssueChecked.
type CloseIssueResult struct {
	Unchanged bool // true when the issue was ALREADY closed (idempotent no-op)
}

// MergeSlotStatus is returned by MergeSlotCheck and describes the current
// state of the merge slot bead.
type MergeSlotStatus struct {
	SlotID    string
	Available bool
	Holder    string
	Waiters   []string
}

// MergeSlotResult is returned by MergeSlotAcquire.
type MergeSlotResult struct {
	// SlotID is the bead ID of the merge slot.
	SlotID string
	// Acquired is true when the slot was successfully acquired by the caller.
	Acquired bool
	// Waiting is true when --wait was passed and the caller was added to the
	// waiters queue (the slot was held by someone else).
	Waiting bool
	// Holder is the current holder of the slot. When Acquired is true this
	// is the caller; when Waiting is true this is the previous holder.
	Holder string
	// Position is the 1-based position in the waiters queue when Waiting is true.
	Position int
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

// GarbageCollector provides Dolt garbage collection capability.
// Callers that need to reclaim disk space should type-assert to this interface.
type GarbageCollector interface {
	DoltGC(ctx context.Context) error
}

// Flattener squashes all Dolt commit history into a single commit.
// Callers should type-assert to this interface for history compaction.
type Flattener interface {
	Flatten(ctx context.Context) error
}

// RemoteRefPruner manages the cached remote-tracking refs that anchor Dolt
// history. After a squash (Flatten/Compact) those refs still point at the
// pre-squash chain, making the follow-up GC a silent no-op on any workspace
// that has ever pushed or fetched (bd-agctw) — callers must prune them before
// GC. Pruning only touches the local cache; the next push/fetch re-creates
// the refs at the new tip. Tags anchor history the same way but are
// user-created, so they are listed for warning rather than deleted.
type RemoteRefPruner interface {
	ListRemoteRefs(ctx context.Context) ([]string, error)
	PruneRemoteRefs(ctx context.Context) ([]string, error)
	ListTags(ctx context.Context) ([]string, error)
}

type SchemaMigrator interface {
	ApplySchemaMigrations(ctx context.Context) (applied int, err error)
}

// Compactor squashes old Dolt commits while preserving recent ones.
// Callers should type-assert to this interface for selective history compaction.
type Compactor interface {
	Compact(ctx context.Context, initialHash, boundaryHash string, oldCommits int, recentHashes []string) error
}

// BlockedRecomputer recomputes the denormalized is_blocked column for every
// issue and wisp in one full pass and reports how many rows it corrected.
// Callers should type-assert to this interface for the is_blocked repair
// (bd-6dnrw.37): unlike the scoped post-pull recompute, it does not depend on a
// merge advancing HEAD, so it can recover a column a skipped recompute (a
// recompute that failed after its merge committed, or a hand-resolved
// conflicted pull) left stale. It is idempotent — a consistent database
// corrects nothing.
type BlockedRecomputer interface {
	RecomputeAllBlocked(ctx context.Context) (int, error)
}

// StateHasher returns a hash covering committed history plus the working set.
// Unlike GetCurrentCommit (HEAD only), the hash moves on uncommitted writes.
// Change detection against a SQL server must use this when available: server
// mode runs with dolt auto-commit off, so writes sit in the working set and
// HEAD does not advance.
// Callers should type-assert to this interface and fall back to
// GetCurrentCommit when the store does not implement it.
type StateHasher interface {
	GetStateHash(ctx context.Context) (string, error)
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

// BackupStore provides Dolt backup operations (CALL DOLT_BACKUP) for
// disaster recovery.
// Callers that need backup functionality should type-assert to this interface.
type BackupStore interface {
	BackupAdd(ctx context.Context, name, url string) error
	BackupSync(ctx context.Context, name string) error
	BackupRemove(ctx context.Context, name string) error
	// BackupDatabase registers dir as a file:// Dolt backup remote and syncs
	// the full database to it, preserving complete commit history.
	BackupDatabase(ctx context.Context, dir string) error
	// RestoreDatabase restores the database from a Dolt backup at dir.
	// When force is true, the existing database is dropped before restoring.
	RestoreDatabase(ctx context.Context, dir string, force bool) error
}

// ReadyWorkCounter sizes the total ready-work count for a filter without
// materializing the counts mega-query. It is identical to
// len(GetReadyWorkWithCounts(filter with Limit=0)) but computed with cheap
// indexed COUNT(*)s over the ready predicate. `bd ready --json` type-asserts to
// this (via UnwrapStore) to render the "Showing X of N" total when a page is
// capped, and falls back to the unbounded GetReadyWorkWithCounts when a store
// does not implement it.
type ReadyWorkCounter interface {
	CountReadyWork(ctx context.Context, filter types.WorkFilter) (int, error)
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
	SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error)     // Narrow projection: returns ids only

	// Dependency operations
	AddDependency(ctx context.Context, dep *types.Dependency, actor string) error
	AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts DependencyAddOptions) error
	RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	// CycleThroughEdges reports a rendered cycle in the static scheduling set
	// (blocks, conditional-blocks, parent-child; not waits-for) that traverses
	// one of the given new edges (issueID -> dependsOnID pairs), or
	// "" when none does. It sees the transaction's own uncommitted dependency
	// writes, which must already include the edges. Lets bulk paths that add
	// edges run one merged whole-graph check before commit and roll back instead
	// of committing cycles (bd-6dnrw.8); pre-existing
	// cycles not using any of the new edges never block (bd-578h9.9).
	CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error)

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

	// Local metadata operations (dolt-ignored, clone-local state).
	// Used for tip timestamps, version stamps, tracker sync cursors, etc.
	// Data is ephemeral — callers must handle ("", nil) as the normal case.
	SetLocalMetadata(ctx context.Context, key, value string) error
	GetLocalMetadata(ctx context.Context, key string) (string, error)

	// Comment operations
	AddComment(ctx context.Context, issueID, actor, comment string) error
	ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error)
	GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error)
}

// DependencyAddOptions controls transaction-scoped dependency insertion.
type DependencyAddOptions struct {
	// SkipCycleCheck bypasses the recursive pre-insert cycle check. Callers
	// that set it MUST run Transaction.CycleThroughEdges before commit and fail
	// on new blocks/conditional-blocks/parent-child cycles (waits-for is excluded) — skipping the per-edge check trades
	// per-edge cost for one whole-graph check, never graph integrity
	// (bd-6dnrw.8).
	SkipCycleCheck bool
}
