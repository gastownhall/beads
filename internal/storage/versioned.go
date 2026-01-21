// Package storage defines the interface for issue storage backends.
package storage

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// VersionedStorage extends Storage with version control capabilities.
// This interface is implemented by storage backends that support history,
// branching, and merging (e.g., Dolt).
//
// Not all storage backends support versioning. Use IsVersioned() to check
// if a storage instance supports these operations before calling them.
type VersionedStorage interface {
	Storage // Embed base interface

	// History queries

	// History returns the complete version history for an issue.
	// Results are ordered by commit date, most recent first.
	History(ctx context.Context, issueID string) ([]*HistoryEntry, error)

	// AsOf returns the state of an issue at a specific commit hash or branch ref.
	// Returns nil if the issue didn't exist at that point in time.
	AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error)

	// Diff returns changes between two commits/branches.
	// Shows which issues were added, modified, or removed.
	Diff(ctx context.Context, fromRef, toRef string) ([]*DiffEntry, error)

	// Branch operations

	// Branch creates a new branch from the current state.
	Branch(ctx context.Context, name string) error

	// Merge merges the specified branch into the current branch.
	// Returns a list of conflicts if any exist.
	Merge(ctx context.Context, branch string) ([]Conflict, error)

	// CurrentBranch returns the name of the currently active branch.
	CurrentBranch(ctx context.Context) (string, error)

	// ListBranches returns the names of all branches.
	ListBranches(ctx context.Context) ([]string, error)

	// Commit operations

	// Commit creates a new commit with all staged changes.
	Commit(ctx context.Context, message string) error

	// GetCurrentCommit returns the hash of the current HEAD commit.
	GetCurrentCommit(ctx context.Context) (string, error)

	// Conflict resolution

	// GetConflicts returns any merge conflicts in the current state.
	GetConflicts(ctx context.Context) ([]Conflict, error)

	// ResolveConflicts resolves conflicts using the specified strategy.
	// Strategy must be "ours" or "theirs".
	ResolveConflicts(ctx context.Context, table string, strategy string) error
}

// HistoryEntry represents an issue at a specific point in history.
type HistoryEntry struct {
	CommitHash string       // The commit hash at this point
	Committer  string       // Who made the commit
	CommitDate time.Time    // When the commit was made
	Issue      *types.Issue // The issue state at that commit
}

// DiffEntry represents a change between two commits.
type DiffEntry struct {
	IssueID  string       // The ID of the affected issue
	DiffType string       // "added", "modified", or "removed"
	OldValue *types.Issue // State before (nil for "added")
	NewValue *types.Issue // State after (nil for "removed")
}

// Conflict represents a merge conflict.
type Conflict struct {
	IssueID     string      // The ID of the conflicting issue
	Field       string      // Which field has the conflict (empty for table-level)
	OursValue   interface{} // Value on current branch
	TheirsValue interface{} // Value on merged branch
}

// IsVersioned checks if a storage instance supports version control operations.
// Returns true if the storage implements VersionedStorage.
//
// Example usage:
//
//	if !storage.IsVersioned(store) {
//	    return fmt.Errorf("history requires Dolt backend")
//	}
//	vs := store.(storage.VersionedStorage)
//	history, err := vs.History(ctx, issueID)
func IsVersioned(s Storage) bool {
	_, ok := s.(VersionedStorage)
	return ok
}

// AsVersioned attempts to cast a Storage to VersionedStorage.
// Returns the VersionedStorage and true if successful, nil and false otherwise.
//
// Example usage:
//
//	vs, ok := storage.AsVersioned(store)
//	if !ok {
//	    return fmt.Errorf("history requires Dolt backend")
//	}
//	history, err := vs.History(ctx, issueID)
func AsVersioned(s Storage) (VersionedStorage, bool) {
	vs, ok := s.(VersionedStorage)
	return vs, ok
}

// RemoteStorage extends VersionedStorage with remote synchronization capabilities.
// This interface is implemented by storage backends that support push/pull to
// remote repositories (e.g., Dolt with DoltHub remotes).
type RemoteStorage interface {
	VersionedStorage

	// Push pushes commits to the configured remote.
	Push(ctx context.Context) error

	// Pull pulls changes from the configured remote.
	Pull(ctx context.Context) error

	// AddRemote adds a new remote with the given name and URL.
	AddRemote(ctx context.Context, name, url string) error
}

// IsRemote checks if a storage instance supports remote synchronization.
// Returns true if the storage implements RemoteStorage.
func IsRemote(s Storage) bool {
	_, ok := s.(RemoteStorage)
	return ok
}

// AsRemote attempts to cast a Storage to RemoteStorage.
// Returns the RemoteStorage and true if successful, nil and false otherwise.
func AsRemote(s Storage) (RemoteStorage, bool) {
	rs, ok := s.(RemoteStorage)
	return rs, ok
}

// FederatedStorage extends RemoteStorage with peer-to-peer federation capabilities.
// This interface supports synchronizing with multiple named peers (towns).
type FederatedStorage interface {
	RemoteStorage

	// PushTo pushes commits to a specific peer remote.
	PushTo(ctx context.Context, peer string) error

	// PullFrom pulls changes from a specific peer remote.
	// Returns any merge conflicts if present.
	PullFrom(ctx context.Context, peer string) ([]Conflict, error)

	// Fetch fetches refs from a peer without merging.
	Fetch(ctx context.Context, peer string) error

	// ListRemotes returns configured remote names and URLs.
	ListRemotes(ctx context.Context) ([]RemoteInfo, error)

	// RemoveRemote removes a configured remote.
	RemoveRemote(ctx context.Context, name string) error

	// SyncStatus returns the sync status with a peer.
	SyncStatus(ctx context.Context, peer string) (*SyncStatus, error)
}

// RemoteInfo describes a configured remote.
type RemoteInfo struct {
	Name string // Remote name (e.g., "town-beta")
	URL  string // Remote URL (e.g., "dolthub://org/repo")
}

// SyncStatus describes the synchronization state with a peer.
type SyncStatus struct {
	Peer         string    // Peer name
	LastSync     time.Time // When last synced
	LocalAhead   int       // Commits ahead of peer
	LocalBehind  int       // Commits behind peer
	HasConflicts bool      // Whether there are unresolved conflicts
}

// IsFederated checks if a storage instance supports federation.
func IsFederated(s Storage) bool {
	_, ok := s.(FederatedStorage)
	return ok
}

// AsFederated attempts to cast a Storage to FederatedStorage.
func AsFederated(s Storage) (FederatedStorage, bool) {
	fs, ok := s.(FederatedStorage)
	return fs, ok
}
