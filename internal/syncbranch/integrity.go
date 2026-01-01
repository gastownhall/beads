// Package syncbranch provides sync branch configuration and integrity checking.
package syncbranch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// Config keys for sync branch integrity tracking
const (
	// RemoteSHAConfigKey stores the last known remote sync branch commit SHA.
	// This is used to detect force pushes on the remote sync branch.
	RemoteSHAConfigKey = "sync.remote_sha"
)

// ForcePushStatus represents the result of a force-push detection check.
type ForcePushStatus struct {
	// Detected is true if a force-push was detected on the remote sync branch.
	Detected bool

	// StoredSHA is the SHA we stored after the last successful sync.
	StoredSHA string

	// CurrentRemoteSHA is the current SHA of the remote sync branch.
	CurrentRemoteSHA string

	// Message provides a human-readable description of the status.
	Message string

	// Branch is the sync branch name.
	Branch string

	// Remote is the remote name (e.g., "origin").
	Remote string
}

// CheckForcePush detects if the remote sync branch has been force-pushed since the last sync.
//
// A force-push is detected when:
// 1. We have a stored remote SHA from a previous sync
// 2. The stored SHA is NOT an ancestor of the current remote SHA
//
// This means the remote history was rewritten (e.g., via force-push, rebase).
//
// Parameters:
//   - ctx: Context for cancellation
//   - store: Storage interface for reading config
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//
// Returns ForcePushStatus with details about the check.
func CheckForcePush(ctx context.Context, store storage.Storage, repoRoot, syncBranch string) (*ForcePushStatus, error) {
	status := &ForcePushStatus{
		Detected: false,
		Branch:   syncBranch,
	}

	// Get stored remote SHA from last sync
	storedSHA, err := store.GetConfig(ctx, RemoteSHAConfigKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get stored remote SHA: %w", err)
	}

	status.StoredSHA = storedSHA

	if storedSHA == "" {
		status.Message = "No previous sync recorded (first sync)"
		return status, nil
	}

	// Get worktree path for git operations
	worktreePath := getBeadsWorktreePath(ctx, repoRoot, syncBranch)

	// Get remote name
	status.Remote = getRemoteForBranch(ctx, worktreePath, syncBranch)

	// Fetch from remote to get latest state
	fetchCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "fetch", status.Remote, syncBranch) // #nosec G204 - repoRoot/syncBranch are validated git inputs
	fetchOutput, err := fetchCmd.CombinedOutput()
	if err != nil {
		// Check if remote branch doesn't exist
		if strings.Contains(string(fetchOutput), "couldn't find remote ref") {
			status.Message = "Remote sync branch does not exist"
			return status, nil
		}
		return nil, fmt.Errorf("failed to fetch remote: %w", err)
	}

	// Get current remote SHA
	remoteRef := fmt.Sprintf("%s/%s", status.Remote, syncBranch)
	revParseCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", remoteRef) // #nosec G204 - remoteRef constructed from trusted config
	revParseOutput, err := revParseCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get remote SHA: %w", err)
	}
	status.CurrentRemoteSHA = strings.TrimSpace(string(revParseOutput))

	// If SHA matches, no change at all
	if storedSHA == status.CurrentRemoteSHA {
		status.Message = "Remote sync branch unchanged since last sync"
		return status, nil
	}

	// Check if stored SHA is an ancestor of current remote SHA
	// This means remote was updated normally (fast-forward)
	isAncestorCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "merge-base", "--is-ancestor", storedSHA, status.CurrentRemoteSHA) // #nosec G204 - args derive from git SHAs we validated earlier
	if isAncestorCmd.Run() == nil {
		// Stored SHA is ancestor - normal update, no force-push
		status.Message = "Remote sync branch updated normally (fast-forward)"
		return status, nil
	}

	// Stored SHA is NOT an ancestor - this indicates a force-push or rebase
	status.Detected = true
	status.Message = fmt.Sprintf(
		"FORCE-PUSH DETECTED: Remote sync branch history was rewritten.\n"+
			"  Previous known commit: %s\n"+
			"  Current remote commit: %s\n"+
			"  The remote history no longer contains your previously synced commit.\n"+
			"  This typically happens when someone force-pushed or rebased the sync branch.",
		storedSHA[:8], status.CurrentRemoteSHA[:8])

	return status, nil
}

// UpdateStoredRemoteSHA stores the current remote sync branch SHA in the database.
// Call this after a successful sync to track the remote state.
//
// Parameters:
//   - ctx: Context for cancellation
//   - store: Storage interface for writing config
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//
// Returns error if the update fails.
func UpdateStoredRemoteSHA(ctx context.Context, store storage.Storage, repoRoot, syncBranch string) error {
	// Get worktree path for git operations
	worktreePath := getBeadsWorktreePath(ctx, repoRoot, syncBranch)

	// Get remote name
	remote := getRemoteForBranch(ctx, worktreePath, syncBranch)

	// Get current remote SHA
	remoteRef := fmt.Sprintf("%s/%s", remote, syncBranch)
	revParseCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", remoteRef) // #nosec G204 - remoteRef is internal config
	revParseOutput, err := revParseCmd.Output()
	if err != nil {
		// Remote branch might not exist yet (first push)
		// Try local branch instead
		revParseCmd = exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", syncBranch) // #nosec G204 - branch name from config
		revParseOutput, err = revParseCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get sync branch SHA: %w", err)
		}
	}
	currentSHA := strings.TrimSpace(string(revParseOutput))

	// Store the SHA
	if err := store.SetConfig(ctx, RemoteSHAConfigKey, currentSHA); err != nil {
		return fmt.Errorf("failed to store remote SHA: %w", err)
	}

	return nil
}

// ClearStoredRemoteSHA removes the stored remote SHA.
// Use this when resetting the sync state (e.g., after accepting a rebase).
func ClearStoredRemoteSHA(ctx context.Context, store storage.Storage) error {
	return store.DeleteConfig(ctx, RemoteSHAConfigKey)
}

// GetStoredRemoteSHA returns the stored remote sync branch SHA.
func GetStoredRemoteSHA(ctx context.Context, store storage.Storage) (string, error) {
	return store.GetConfig(ctx, RemoteSHAConfigKey)
}
