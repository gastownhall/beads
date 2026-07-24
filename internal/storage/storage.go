// Package storage provides shared types for issue storage.
//
// The concrete storage implementation lives in the dolt sub-package.
// This package holds interface and value types that are referenced by
// both the dolt implementation and its consumers (cmd/bd, etc.).
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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

// ClaimedByFragment and NotClaimableStatusFragment are the exact message
// fragments the claim path (issueops/claim.go) appends after the sentinel to
// carry the conflicting assignee/status: ErrAlreadyClaimed is wrapped as
// "<sentinel> by <assignee>" and ErrNotClaimable as "<sentinel>: status
// <status>". They are the single source of truth for that format so producer
// (claim.go) and consumer (beads.ParseClaimConflict) cannot drift: the consumer
// reconstructs its marker as ErrAlreadyClaimed.Error()+ClaimedByFragment rather
// than hardcoding the literal.
const (
	ClaimedByFragment          = " by "
	NotClaimableStatusFragment = ": status "
)

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

// ErrVersionMismatch is returned by a *Checked op given an ExpectedVersion that
// no longer matches the row's current version (row_lock) — an optimistic
// concurrency failure. Callers errors.Is it to distinguish a lost-update
// precondition from other errors.
var ErrVersionMismatch = errors.New("version mismatch")

// CommentPageCursor is the resume position for a keyset page of an issue's
// comments: the (created_at, id) of the last comment already returned. The zero
// value starts a walk from the beginning of the thread.
//
// It lives in the storage package (rather than issueops) because issueops
// imports storage — the reverse would be an import cycle — so the shared cursor
// type is defined here and referenced from the issueops query layer.
type CommentPageCursor struct {
	CreatedAt time.Time
	ID        string
}

// Storage is the interface satisfied by *dolt.DoltStore.
// Consumers depend on this interface rather than on the concrete type so that
// alternative implementations (mocks, proxies, etc.) can be substituted.
//
// External implementers note: this contract includes the optimistic-concurrency
// helper UpdateIssueChecked and the atomic MergeMetadata method as required
// members. Adding a required method is a breaking change for out-of-tree
// implementations; such additions are called out in CHANGELOG.md and the
// examples/library-usage guide so implementers have a migration path.
type Storage interface {
	// Issue CRUD
	CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
	CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error)
	GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
	// UpdateIssueChecked applies the update like UpdateIssue, with an optional
	// optimistic-concurrency precondition: see UpdateIssueOptions.ExpectedVersion.
	// The version read and the update share one transaction (a true CAS).
	UpdateIssueChecked(ctx context.Context, id string, updates map[string]interface{}, actor string, opts UpdateIssueOptions) error
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
	// the issue has a live direct blocker (an open blocks/waits-for/
	// conditional-blocks edge) unless opts.Force is set — the historical
	// `bd close` guard. A bare is_blocked=1 with no live direct blocker (a purely
	// transitive parent-child block, or a stale column) is not refused. The
	// blocked-check and the close run in ONE transaction, so the guard is atomic
	// (no TOCTOU). When opts.ExpectedVersion is non-nil it adds an orthogonal
	// optimistic-concurrency precondition: the close proceeds only if the issue's
	// current RowVersion still equals *opts.ExpectedVersion, else it refuses with
	// ErrVersionMismatch atomically (Force does NOT bypass this check). Already-
	// closed is an idempotent success with Unchanged=true; a missing issue returns
	// ErrNotFound.
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
	// AddDependencyWithOptions adds a dependency with explicit options. The
	// explicit dependency verbs (bd dep add / bd link) pass EmitEvent to record
	// a dependency_added history event; AddDependency is the no-event default
	// used by create-with-deps and structural callers.
	AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts DependencyAddOptions) error
	RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error
	// RemoveDependencyWithOptions removes a dependency with explicit options. The
	// explicit dependency verb (bd dep remove) passes EmitEvent to record a
	// dependency_removed history event; RemoveDependency is the no-event default
	// used by structural callers (issue delete, reparent, batch, duplicate cleanup).
	RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts DependencyRemoveOptions) error
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
	// GetIssueCommentsPage returns one keyset page of an issue's comments in the
	// stable (created_at ASC, id ASC) total order, resuming strictly after the
	// after cursor (the zero cursor starts from the beginning of the thread).
	// id is the primary key, so the same-second tie-break is total: a thread
	// with several comments in the same created_at second still pages
	// completely, and concatenating every page of a full walk yields exactly the
	// same comments in the same order as GetIssueComments — no dropped or
	// duplicated comment. The resume predicate is sargable: it seeks the
	// (issue_id, created_at, id) index rather than scanning the whole thread.
	//
	// The after cursor MUST come from a comment previously returned by a read
	// (this method or GetIssueComments), whose CreatedAt matches the stored
	// DATETIME second. Feeding a cursor with a sub-second CreatedAt can skip
	// same-second rows (AddIssueComment already truncates its returned CreatedAt
	// for this reason).
	//
	// Keyset semantics, like an audit feed: a comment inserted with a backdated
	// created_at that lands behind an in-progress cursor is not seen by that
	// walk — the walk only moves forward. A whole-thread read or a fresh walk
	// still returns it.
	//
	// limit <= 0 uses a store default (100); a larger limit is capped at 500. A
	// caller that pages until len(page) < limit must therefore keep limit <= 500
	// or use empty-page termination instead: a request for limit > 500 always
	// returns at most 500 rows and would stop a len-based loop one page early.
	GetIssueCommentsPage(ctx context.Context, issueID string, after CommentPageCursor, limit int) ([]*types.Comment, error)
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

	// MergeMetadata merges a single key into an issue's metadata JSON as a raw
	// JSON value (nested objects/arrays are preserved). The read-modify-write
	// runs in a single transaction, so two concurrent merges of DIFFERENT keys
	// both survive rather than clobbering each other. SlotSet is built on it.
	MergeMetadata(ctx context.Context, issueID, key string, value json.RawMessage, actor string) error

	// Lifecycle
	Close() error
}

// CloseIssueOptions carries the optional inputs to CloseIssueChecked.
type CloseIssueOptions struct {
	Reason  string
	Session string
	Force   bool // bypass the is_blocked guard (mirrors `bd close --force`)
	// ExpectedVersion, when non-nil, gates the close on an optimistic-concurrency
	// check: the close proceeds only if the issue's current RowVersion (the
	// row_lock token) equals *ExpectedVersion, otherwise it refuses with
	// ErrVersionMismatch atomically (the version read and the close share one
	// transaction). nil disables the check, leaving behavior unchanged. It is a
	// pointer, not an int64, so nil ("no check") is distinct from a caller that
	// requires version 0. Force bypasses only the is_blocked guard, not this
	// version check.
	//
	// RowVersion tracks lifecycle/ownership writes only — it is rewritten by
	// status, assignee, and started_at changes (claim, close, reclaim, unclaim,
	// updateIssueInTx). So this is a "close only if the issue's lifecycle state
	// is unchanged" guard, NOT an all-columns check: concurrent label, dependency,
	// rename, is_blocked, or compaction-only writes intentionally do not bump
	// row_lock and are not caught here (see the freshRowLock invariant in
	// internal/storage/issueops/lease.go).
	ExpectedVersion *int64
}

// CloseIssueResult reports the outcome of CloseIssueChecked.
type CloseIssueResult struct {
	Unchanged bool // true when the issue was ALREADY closed (idempotent no-op)
}

// UpdateIssueOptions carries the optional inputs to UpdateIssueChecked.
type UpdateIssueOptions struct {
	// ExpectedVersion, when non-nil, makes the update a compare-and-swap: it
	// proceeds only if the issue's current RowVersion (row_lock) equals
	// *ExpectedVersion, else it refuses with ErrVersionMismatch atomically.
	// nil disables the check. A pointer so nil is distinct from requiring a
	// legacy version of 0.
	ExpectedVersion *int64
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
	EventQueryStore
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
// # Compose surface (classic path)
//
// The transaction methods are implemented by the classic Dolt and
// embedded-Dolt stores. The domain/uow plumbing (internal/storage/domain) is a
// separate compose surface that does not implement storage.Transaction today;
// that asymmetry is pre-existing and out of scope for this surface.
//
// The read methods below let a caller assemble a whole composite view — a
// bd show-style assembly of counts and relations — inside ONE transaction, so
// everything it stitches together is read from a single snapshot and cannot
// tear across separate engine reads.
//
// TWO-SESSION WISP CAVEAT (server/Dolt backend only): the classic Dolt store
// runs durable tables and dolt-ignored wisp tables on two separate SQL sessions
// within one logical transaction. Reads that span both tiers in a single query
// (the ones flagged below) therefore see this transaction's own uncommitted
// DURABLE writes and all COMMITTED wisps, but NOT wisps written in the same
// still-open transaction — those become visible after commit. Single-tier reads
// (GetIssue, GetIssueComments, GetIssueCommentsPage, GetDependencyRecords,
// IsBlocked, IsBlockedBatch, GetLabels) route to the owning session and are
// read-your-writes on both tiers. The embedded-Dolt store has no session split,
// so every read there is read-your-writes on both tiers.
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
	// RemoveDependencyWithOptions removes a dependency with explicit options.
	// EmitEvent records a dependency_removed history event for the explicit
	// bd dep remove verb; RemoveDependency stays silent for structural teardown.
	RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts DependencyRemoveOptions) error
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
	GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) // For read-your-writes within transaction
	// GetIssueCommentsPage returns one keyset page of an issue's comments in the
	// stable (created_at ASC, id ASC) order, resuming strictly after the cursor
	// (the zero cursor starts at the beginning of the thread). Lets a composite
	// view page a comment thread off the same snapshot as its other reads. See
	// storage.Storage.GetIssueCommentsPage for the full ordering and
	// page-walk-equals-full-read contract.
	GetIssueCommentsPage(ctx context.Context, issueID string, after CommentPageCursor, limit int) ([]*types.Comment, error)

	// Composite-view reads.
	//
	// Each mirrors the Storage-level method of the same name; they add no new
	// query shape, only the ability to run the existing read on the
	// transaction's snapshot, so a bd show-style assembly can gather every count
	// and relation it needs inside one transaction. All see this transaction's
	// own uncommitted DURABLE writes; the wisp-tier visibility of the
	// both-tiers-spanning reads is governed by the TWO-SESSION WISP CAVEAT above.

	// CountIssuesByGroup returns per-group issue counts. groupBy is one of:
	// status, priority, type, assignee, label. SPANS BOTH TIERS (merges wisps):
	// subject to the two-session wisp caveat on the server backend. Note it merges
	// committed wisps into the buckets while the transaction's SearchIssues reads
	// the issues table only, so their totals need not agree when committed wisps
	// exist — a pre-existing count-vs-search wisp-scoping asymmetry, not a tear.
	CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error)

	// GetDependentRecords returns the raw inbound dependency rows whose target is
	// targetID (its dependents), spanning the durable and wisp dependency tables,
	// filtered by depType ("" = all), bounded by limit and paged by afterID.
	// SPANS BOTH TIERS: subject to the two-session wisp caveat on the server backend.
	GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error)
	// GetDependentRecordsForIssues returns the raw inbound dependency rows for a
	// SET of target ids in one batched read, keyed by target id. SPANS BOTH TIERS:
	// subject to the two-session wisp caveat on the server backend.
	GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error)
	// CountDependentRecords returns the total inbound-edge count of targetID
	// across both dependency tables (same predicate/scope as GetDependentRecords).
	// SPANS BOTH TIERS: subject to the two-session wisp caveat on the server backend.
	CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error)

	// IsBlocked reports the denormalized transitive is_blocked flag for one issue
	// plus its direct blocker ids. Single-tier (routes to the issue's own tier):
	// read-your-writes on both tiers.
	IsBlocked(ctx context.Context, issueID string) (bool, []string, error)
	// IsBlockedBatch reports the denormalized transitive is_blocked flag for a
	// page of ids in one batched read. ids present in neither the issues nor the
	// wisps table are absent from the map; callers treat absent as not-blocked.
	// Partitions ids by tier and reads each on its owning session, so it is
	// read-your-writes on both tiers even for a mixed durable/wisp batch.
	IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error)

	// EventsSince returns durable events strictly after cursor, ordered by
	// (created_at ASC, id ASC) and bounded by limit; issueID scopes the feed to
	// one issue's history ("" = all issues). Durable events table only.
	EventsSince(ctx context.Context, cursor EventCursor, issueID string, limit int) ([]*types.Event, error)
}

// DependencyAddOptions controls dependency insertion for both the store-level
// AddDependencyWithOptions and the transaction-scoped AddDependencyWithOptions.
type DependencyAddOptions struct {
	// SkipCycleCheck bypasses the recursive pre-insert cycle check. Callers
	// that set it MUST run Transaction.CycleThroughEdges before commit and fail
	// on new blocks/conditional-blocks/parent-child cycles (waits-for is excluded) — skipping the per-edge check trades
	// per-edge cost for one whole-graph check, never graph integrity
	// (bd-6dnrw.8).
	SkipCycleCheck bool
	// EmitEvent records a dependency_added history event on the source's event
	// table for a genuine new edge. Only the explicit dependency verbs set it;
	// create-with-deps and structural edge wiring leave it unset so implicit
	// edges stay quiet, matching the proxied DepInsertOpts.EmitEvent gate.
	EmitEvent bool
}

// DependencyRemoveOptions controls dependency removal for both the store-level
// RemoveDependencyWithOptions and the transaction-scoped RemoveDependencyWithOptions.
type DependencyRemoveOptions struct {
	// EmitEvent records a dependency_removed history event on the source's event
	// table when a genuine edge is removed. Only the explicit bd dep remove verb
	// sets it; structural removals (issue delete, reparent, batch, duplicate
	// cleanup) leave it unset so they wire edges away quietly, matching the
	// proxied DepInsertOpts.EmitEvent gate so both backends record identical
	// history.
	EmitEvent bool
}
