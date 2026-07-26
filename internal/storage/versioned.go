// Package storage provides shared types for issue storage.
package storage

import (
	"time"

	"github.com/steveyegge/beads/internal/types"
)

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
	// Count is how many rows of the table are conflicted, as reported by
	// dolt_conflicts.num_conflicts. Meaningful for table-level conflicts
	// (the shape GetConflicts returns); zero otherwise.
	Count int
}

// RemoteInfo describes a configured remote.
type RemoteInfo struct {
	Name string `json:"name"` // Remote name (e.g., "town-beta")
	URL  string `json:"url"`  // Remote URL (e.g., "dolthub://org/repo")
}

// RebaseRenumber records a single child issue that was renumbered to clear a
// cross-clone hierarchical-ID collision. When the renumbered issue had its own
// descendants, those were rewritten under NewID too (reported only by the
// top-of-subtree row here).
type RebaseRenumber struct {
	OldID  string `json:"old_id"`
	NewID  string `json:"new_id"`
	Parent string `json:"parent"`
}

// RebaseReport summarizes what `bd dolt rebase` did: which side dominated, the
// pre-mutation backup tag, every renumbered child, and the final child-counter
// high-water set per affected parent. Renumbered is empty when there were no
// collisions to resolve (the caller should fall back to a plain pull).
type RebaseReport struct {
	Direction   string           `json:"direction"` // "remote-dominates" | "local-dominates"
	BackupTag   string           `json:"backup_tag"`
	Renumbered  []RebaseRenumber `json:"renumbered"`
	CountersSet map[string]int   `json:"counters_set"`
}

// SyncStatus describes the synchronization state with a peer.
type SyncStatus struct {
	Peer         string    // Peer name
	LastSync     time.Time // When last synced
	LocalAhead   int       // Commits ahead of peer
	LocalBehind  int       // Commits behind peer
	HasConflicts bool      // Whether there are unresolved conflicts
}

// FederationPeer represents a remote peer with authentication credentials.
// Used for peer-to-peer Dolt remotes between workspaces with SQL user auth.
type FederationPeer struct {
	Name        string     // Unique name for this peer (used as remote name)
	RemoteURL   string     // Dolt remote URL (e.g., http://host:port/org/db)
	Username    string     // SQL username for authentication
	Password    string     // Password (decrypted, not stored directly)
	Sovereignty string     // Sovereignty tier: T1, T2, T3, T4
	LastSync    *time.Time // Last successful sync time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
