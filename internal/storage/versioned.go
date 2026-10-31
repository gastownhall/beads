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
	// Ref is the git ref a git-backed remote keeps its Dolt data on, when the
	// remote was added with one. Empty means DefaultGitDataRef; always empty
	// for remotes that are not git-backed.
	Ref string `json:"ref,omitempty"`
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

// RebaseOptions selects how `bd dolt rebase` reconciles a collision.
//
// It is a struct rather than two bool parameters because the two settings are
// independent and a call site reading RebaseRemote(ctx, "origin", true, false)
// says nothing about which "true" is which.
type RebaseOptions struct {
	// LocalDominates picks the side that keeps its ids: false (the default,
	// remote-dominates) renumbers the LOCAL colliding children, true renumbers
	// the remote's.
	LocalDominates bool
	// DryRun reports what a real run would do and changes nothing at all: no
	// auto-commit of pending work, no backup tag, no renumber, no merge — and
	// no fetch either.
	//
	// Not fetching is deliberate rather than a limitation. cmd/bd classifies
	// any command carrying a --dry-run flag as a preview and opens the store
	// READ-ONLY (isPreviewCommand, main.go), and a fetch is a write: it moves a
	// remote-tracking ref. So a dry run reads the state the LAST fetch left,
	// which is exactly the state an operator has when they need this — a
	// `bd dolt pull` that aborts on the collision has already fetched. Where it
	// is not, `bd dolt fetch` is the explicit one-liner, and the mechanic says
	// so rather than guessing.
	DryRun bool
}

// RebaseReport summarizes what `bd dolt rebase` did: which side dominated, the
// pre-mutation backup tag, every renumbered child, and the final child-counter
// high-water set per affected parent. Renumbered is empty when there were no
// collisions to resolve (the caller should fall back to a plain pull).
//
// Restored distinguishes the two ways a rebase can return an error. When true,
// the reconcile itself failed and the branch was hard-reset back to BackupTag —
// nothing was kept, and Renumbered records what was rolled back. When false, the
// reconcile committed successfully and a LATER step failed (e.g. the post-merge
// is_blocked recompute): the merge stands and Renumbered is live. Callers must
// not report a rollback that did not happen.
//
// On a DryRun report, Renumbered is the PLAN — the renumbers a real run would
// perform, computed by the same function that performs them — and BackupTag is
// empty because nothing was tagged.
type RebaseReport struct {
	Direction   string           `json:"direction"` // "remote-dominates" | "local-dominates"
	BackupTag   string           `json:"backup_tag"`
	Renumbered  []RebaseRenumber `json:"renumbered"`
	CountersSet map[string]int   `json:"counters_set"`
	Restored    bool             `json:"restored"`
	// DryRun is true when nothing was mutated and Renumbered is a plan.
	DryRun bool `json:"dry_run"`
	// TopLevelCollisions names top-level (non-hierarchical) ids that both sides
	// minted independently for DIFFERENT issues. This mechanic does not repair
	// them — see RebaseOptions and the command's help for why — but a merge
	// cannot settle an add/add either, so reporting them turns an opaque
	// "conflicts require operator resolution" abort into a named diagnosis.
	TopLevelCollisions []string `json:"top_level_collisions,omitempty"`
	// Breadcrumbs counts the notes appended to renumbered-and-contested ids
	// recording that the id moved and that inbound text references to it are
	// now ambiguous.
	Breadcrumbs int `json:"breadcrumbs"`
	// BreadcrumbWarning is set when the reconcile succeeded but those notes
	// could not be written. The reconcile is NOT rolled back for it: a missing
	// breadcrumb is a lost annotation, while undoing a committed merge would
	// re-block the sync the rebase just unblocked.
	BreadcrumbWarning string `json:"breadcrumb_warning,omitempty"`
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
