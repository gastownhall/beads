package syncbranch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/merge"
)

// CommitResult contains information about a worktree commit operation
type CommitResult struct {
	Committed  bool   // True if changes were committed
	Pushed     bool   // True if changes were pushed
	Branch     string // The sync branch name
	Message    string // Commit message used
}

// DivergenceInfo contains information about sync branch divergence from remote
type DivergenceInfo struct {
	LocalAhead   int    // Number of commits local is ahead of remote
	RemoteAhead  int    // Number of commits remote is ahead of local
	Branch       string // The sync branch name
	Remote       string // The remote name (e.g., "origin")
	IsDiverged   bool   // True if both local and remote have commits the other doesn't
	IsSignificant bool  // True if divergence exceeds threshold (suggests recovery needed)
}

// SignificantDivergenceThreshold is the number of commits at which divergence is considered significant
// When both local and remote are ahead by at least this many commits, the user should consider recovery options
const SignificantDivergenceThreshold = 5

// PullResult contains information about a worktree pull operation
type PullResult struct {
	Pulled        bool   // True if pull was performed
	Branch        string // The sync branch name
	JSONLPath     string // Path to the synced JSONL in main repo
	Merged        bool   // True if divergent histories were merged
	FastForwarded bool   // True if fast-forward was possible
	Pushed        bool   // True if changes were pushed after merge

	// SafetyCheckTriggered indicates mass deletion was detected during merge
	// When true, callers should check config option sync.require_confirmation_on_mass_delete
	SafetyCheckTriggered bool
	// SafetyCheckDetails contains human-readable details about the mass deletion
	SafetyCheckDetails string
	// SafetyWarnings contains warning messages from the safety check
	// Caller should display these to the user as appropriate for their output format
	SafetyWarnings []string
}

// CommitToSyncBranch commits JSONL changes to the sync branch using a git worktree.
// This allows committing to a different branch without changing the user's working directory.
//
// IMPORTANT: Before committing, this function now performs a pre-emptive fetch
// and fast-forward if possible. This reduces the likelihood of divergence by ensuring we're
// building on top of the latest remote state when possible.
//
// Parameters:
//   - ctx: Context for cancellation
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//   - jsonlPath: Absolute path to the JSONL file in the main repo
//   - push: If true, push to remote after commit
//
// Returns CommitResult with details about what was done, or error if failed.
func CommitToSyncBranch(ctx context.Context, repoRoot, syncBranch, jsonlPath string, push bool) (*CommitResult, error) {
	result := &CommitResult{
		Branch: syncBranch,
	}

	// GH#639: Use git-common-dir for worktree path to support bare repos
	worktreePath := getBeadsWorktreePath(ctx, repoRoot, syncBranch)

	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)

	// Ensure worktree exists and is healthy
	// CreateBeadsWorktree performs a full health check internally and
	// automatically repairs unhealthy worktrees by removing and recreating them
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	// Get remote name
	remote := getRemoteForBranch(ctx, worktreePath, syncBranch)

	// Pre-emptive fetch and fast-forward
	// This reduces divergence by ensuring we commit on top of latest remote state
	if err := preemptiveFetchAndFastForward(ctx, worktreePath, syncBranch, remote); err != nil {
		// Non-fatal: if fetch fails (e.g., offline), we can still commit locally
		// The divergence will be handled during the next pull
		_ = err
	}

	// Convert absolute path to relative path from repo root
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}

	// Sync JSONL file to worktree
	if err := wtMgr.SyncJSONLToWorktree(worktreePath, jsonlRelPath); err != nil {
		return nil, fmt.Errorf("failed to sync JSONL to worktree: %w", err)
	}

	// Also sync other beads files (metadata.json)
	beadsDir := filepath.Dir(jsonlPath)
	for _, filename := range []string{"metadata.json"} {
		srcPath := filepath.Join(beadsDir, filename)
		if _, err := os.Stat(srcPath); err == nil {
			relPath, err := filepath.Rel(repoRoot, srcPath)
			if err == nil {
				_ = wtMgr.SyncJSONLToWorktree(worktreePath, relPath) // Best effort
			}
		}
	}

	// Check for changes in worktree
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	hasChanges, err := hasChangesInWorktree(ctx, worktreePath, worktreeJSONLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check for changes in worktree: %w", err)
	}

	if !hasChanges {
		return result, nil // No changes to commit
	}

	// Commit in worktree
	result.Message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	if err := commitInWorktree(ctx, worktreePath, jsonlRelPath, result.Message); err != nil {
		return nil, fmt.Errorf("failed to commit in worktree: %w", err)
	}
	result.Committed = true

	// Push if enabled
	if push {
		if err := pushFromWorktree(ctx, worktreePath, syncBranch); err != nil {
			return nil, fmt.Errorf("failed to push from worktree: %w", err)
		}
		result.Pushed = true
	}

	return result, nil
}

// preemptiveFetchAndFastForward fetches from remote and fast-forwards if possible.
// This reduces divergence by keeping the local sync branch up-to-date before committing.
// Returns nil on success, or error if fetch/ff fails (caller should treat as non-fatal).
func preemptiveFetchAndFastForward(ctx context.Context, worktreePath, branch, remote string) error {
	// Fetch from remote
	fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, branch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		// Check if remote branch doesn't exist yet (first sync)
		if strings.Contains(string(output), "couldn't find remote ref") {
			return nil // Not an error - remote branch doesn't exist yet
		}
		return fmt.Errorf("fetch failed: %w", err)
	}

	// Check if we can fast-forward
	localAhead, remoteAhead, err := getDivergence(ctx, worktreePath, branch, remote)
	if err != nil {
		return fmt.Errorf("divergence check failed: %w", err)
	}

	// If remote has new commits and we have no local commits, fast-forward
	if remoteAhead > 0 && localAhead == 0 {
		mergeCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge", "--ff-only",
			fmt.Sprintf("%s/%s", remote, branch))
		if output, err := mergeCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("fast-forward failed: %w\n%s", err, output)
		}
	}

	return nil
}

// PullFromSyncBranch pulls changes from the sync branch and copies JSONL to the main repo.
// This fetches remote changes without affecting the user's working directory.
//
// IMPORTANT: This function handles diverged histories gracefully by performing
// a content-based merge instead of relying on git's commit-level merge. When local and remote
// sync branches have diverged:
//  1. Fetch remote changes (don't pull)
//  2. Find the merge base
//  3. Extract JSONL from base, local, and remote
//  4. Perform 3-way content merge using bd's merge algorithm
//  5. Reset to remote's history (adopt remote commit graph)
//  6. Commit merged content on top
//
// IMPORTANT: After successful content merge, auto-pushes to remote by default.
// Includes safety check: warns (but doesn't block) if >50% issues vanished AND >5 existed.
// "Vanished" means removed from issues.jsonl entirely, NOT status=closed.
//
// IMPORTANT: If requireMassDeleteConfirmation is true and the safety check triggers,
// the function will NOT auto-push. Instead, it sets SafetyCheckTriggered=true in the result
// and the caller should prompt for confirmation then call PushSyncBranch.
//
// This ensures sync never fails due to git merge conflicts, as we handle merging at the
// JSONL content level where we have semantic understanding of the data.
//
// Parameters:
//   - ctx: Context for cancellation
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//   - jsonlPath: Absolute path to the JSONL file in the main repo
//   - push: If true, push to remote after merge
//   - requireMassDeleteConfirmation: If true and mass deletion detected, skip push
//
// Returns PullResult with details about what was done, or error if failed.
func PullFromSyncBranch(ctx context.Context, repoRoot, syncBranch, jsonlPath string, push bool, requireMassDeleteConfirmation ...bool) (*PullResult, error) {
	// Extract optional confirmation requirement parameter
	requireConfirmation := false
	if len(requireMassDeleteConfirmation) > 0 {
		requireConfirmation = requireMassDeleteConfirmation[0]
	}

	result := &PullResult{
		Branch:    syncBranch,
		JSONLPath: jsonlPath,
	}

	// GH#639: Use git-common-dir for worktree path to support bare repos
	worktreePath := getBeadsWorktreePath(ctx, repoRoot, syncBranch)

	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)

	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	// Get remote name
	remote := getRemoteForBranch(ctx, worktreePath, syncBranch)

	// Convert absolute path to relative path from repo root
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}

	// Step 1: Fetch from remote (don't pull - we handle merge ourselves)
	fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, syncBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		// Check if remote branch doesn't exist yet (first sync)
		if strings.Contains(string(output), "couldn't find remote ref") {
			// Remote branch doesn't exist - nothing to pull
			result.Pulled = false
			return result, nil
		}
		return nil, fmt.Errorf("git fetch failed in worktree: %w\n%s", err, output)
	}

	// Step 2: Check for divergence
	localAhead, remoteAhead, err := getDivergence(ctx, worktreePath, syncBranch, remote)
	if err != nil {
		return nil, fmt.Errorf("failed to check divergence: %w", err)
	}

	// Case 1: Already up to date (remote has nothing new)
	if remoteAhead == 0 {
		result.Pulled = true
		// Still copy JSONL in case worktree has uncommitted changes
		if err := copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Case 2: Can fast-forward (we have no local commits ahead of remote)
	if localAhead == 0 {
		// Simple fast-forward merge
		mergeCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge", "--ff-only",
			fmt.Sprintf("%s/%s", remote, syncBranch))
		if output, err := mergeCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git merge --ff-only failed: %w\n%s", err, output)
		}
		result.Pulled = true
		result.FastForwarded = true

		// Copy JSONL to main repo
		if err := copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Case 3: DIVERGED - perform content-based merge
	// This is the key fix: instead of git merge (which can fail), we:
	// 1. Extract JSONL content from base, local, and remote
	// 2. Merge at content level using our 3-way merge algorithm
	// 3. Reset to remote's commit history
	// 4. Commit merged content on top

	// Extract local content before merge for safety check
	localContent, extractErr := extractJSONLFromCommit(ctx, worktreePath, "HEAD", jsonlRelPath)
	if extractErr != nil {
		// Add warning to result so callers can display appropriately
		result.SafetyWarnings = append(result.SafetyWarnings,
			fmt.Sprintf("⚠️  Warning: Could not extract local content for safety check: %v", extractErr))
	}

	mergedContent, err := performContentMerge(ctx, worktreePath, syncBranch, remote, jsonlRelPath)
	if err != nil {
		return nil, fmt.Errorf("content merge failed: %w", err)
	}

	// Reset worktree to remote's history (adopt their commit graph)
	resetCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "reset", "--hard",
		fmt.Sprintf("%s/%s", remote, syncBranch))
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git reset failed: %w\n%s", err, output)
	}

	// Write merged content
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	if err := os.MkdirAll(filepath.Dir(worktreeJSONLPath), 0750); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(worktreeJSONLPath, mergedContent, 0600); err != nil {
		return nil, fmt.Errorf("failed to write merged JSONL: %w", err)
	}

	// Check if merge produced any changes from remote
	hasChanges, err := hasChangesInWorktree(ctx, worktreePath, worktreeJSONLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check for changes: %w", err)
	}

	// Commit merged content if there are changes
	if hasChanges {
		message := fmt.Sprintf("bd sync: merge divergent histories (%d local + %d remote commits)",
			localAhead, remoteAhead)
		if err := commitInWorktree(ctx, worktreePath, jsonlRelPath, message); err != nil {
			return nil, fmt.Errorf("failed to commit merged content: %w", err)
		}
	}

	result.Pulled = true
	result.Merged = true

	// Copy merged JSONL to main repo
	if err := copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath); err != nil {
		return nil, err
	}

	// Auto-push after successful content merge
	if push && hasChanges {
		// Safety check: count issues before and after merge to detect mass deletion
		localCount := countIssuesInContent(localContent)
		mergedCount := countIssuesInContent(mergedContent)

		// Track if we should skip push due to safety check requiring confirmation
		skipPushForConfirmation := false

		// Warn if >50% issues vanished AND >5 existed before
		// "Vanished" = removed from JSONL entirely (not status=closed)
		if localCount > 5 && mergedCount < localCount {
			vanishedPercent := float64(localCount-mergedCount) / float64(localCount) * 100
			if vanishedPercent > 50 {
				// Set safety check fields for caller to handle confirmation
				result.SafetyCheckTriggered = true
				result.SafetyCheckDetails = fmt.Sprintf("%.0f%% of issues vanished during merge (%d → %d issues)",
					vanishedPercent, localCount, mergedCount)

				// Return warnings in result instead of printing directly to stderr
				result.SafetyWarnings = append(result.SafetyWarnings,
					fmt.Sprintf("⚠️  Warning: %.0f%% of issues vanished during merge (%d → %d issues)",
						vanishedPercent, localCount, mergedCount))

				// Add forensic info to warnings
				localIssues := parseIssuesFromContent(localContent)
				mergedIssues := parseIssuesFromContent(mergedContent)
				forensicLines := formatVanishedIssues(localIssues, mergedIssues, localCount, mergedCount)
				result.SafetyWarnings = append(result.SafetyWarnings, forensicLines...)

				// Check if confirmation is required before pushing
				if requireConfirmation {
					result.SafetyWarnings = append(result.SafetyWarnings,
						"   Push skipped - confirmation required (sync.require_confirmation_on_mass_delete=true)")
					skipPushForConfirmation = true
				} else {
					result.SafetyWarnings = append(result.SafetyWarnings,
						"   This may indicate accidental mass deletion. Pushing anyway.",
						"   If this was unintended, use 'git reflog' on the sync branch to recover.")
				}
			}
		}

		// Push unless safety check requires confirmation
		if !skipPushForConfirmation {
			if err := pushFromWorktree(ctx, worktreePath, syncBranch); err != nil {
				return nil, fmt.Errorf("failed to push after merge: %w", err)
			}
			result.Pushed = true
		}
	}

	return result, nil
}

// getDivergence returns how many commits local is ahead and behind remote.
// Returns (localAhead, remoteAhead, error)
func getDivergence(ctx context.Context, worktreePath, branch, remote string) (int, int, error) {
	// Use rev-list to count commits in each direction
	// --left-right --count gives us "local\tremote"
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-list",
		"--left-right", "--count",
		fmt.Sprintf("HEAD...%s/%s", remote, branch))
	output, err := cmd.Output()
	if err != nil {
		// If this fails, remote branch might not exist locally yet
		// Check if it's a tracking issue
		return 0, 0, fmt.Errorf("failed to get divergence: %w", err)
	}

	// Parse "N\tM" format
	parts := strings.Fields(strings.TrimSpace(string(output)))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %s", output)
	}

	localAhead, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse local ahead count: %w", err)
	}

	remoteAhead, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse remote ahead count: %w", err)
	}

	return localAhead, remoteAhead, nil
}

// CheckDivergence checks the divergence between local sync branch and remote.
// This should be called before attempting sync operations to detect significant divergence
// that may require user intervention.
//
// Parameters:
//   - ctx: Context for cancellation
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//
// Returns DivergenceInfo with details about the divergence, or error if check fails.
func CheckDivergence(ctx context.Context, repoRoot, syncBranch string) (*DivergenceInfo, error) {
	info := &DivergenceInfo{
		Branch: syncBranch,
	}

	// GH#639: Use git-common-dir for worktree path to support bare repos
	worktreePath := getBeadsWorktreePath(ctx, repoRoot, syncBranch)

	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)

	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	// Get remote name
	remote := getRemoteForBranch(ctx, worktreePath, syncBranch)
	info.Remote = remote

	// Fetch from remote to get latest state
	fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, syncBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		// Check if remote branch doesn't exist yet (first sync)
		if strings.Contains(string(output), "couldn't find remote ref") {
			// Remote branch doesn't exist - no divergence possible
			return info, nil
		}
		return nil, fmt.Errorf("git fetch failed: %w\n%s", err, output)
	}

	// Check for divergence
	localAhead, remoteAhead, err := getDivergence(ctx, worktreePath, syncBranch, remote)
	if err != nil {
		return nil, fmt.Errorf("failed to check divergence: %w", err)
	}

	info.LocalAhead = localAhead
	info.RemoteAhead = remoteAhead
	info.IsDiverged = localAhead > 0 && remoteAhead > 0

	// Significant divergence: both sides have many commits
	// This suggests automatic merge may be problematic
	if info.IsDiverged && (localAhead >= SignificantDivergenceThreshold || remoteAhead >= SignificantDivergenceThreshold) {
		info.IsSignificant = true
	}

	return info, nil
}

// ResetToRemote resets the local sync branch to match the remote state.
// This discards all local commits on the sync branch and adopts the remote's history.
// Use this when the sync branch has diverged significantly and you want to discard local changes.
//
// Parameters:
//   - ctx: Context for cancellation
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//   - jsonlPath: Path to the JSONL file in the main repo (will be updated with remote content)
//
// Returns error if reset fails.
func ResetToRemote(ctx context.Context, repoRoot, syncBranch, jsonlPath string) error {
	// GH#639: Use git-common-dir for worktree path to support bare repos
	worktreePath := getBeadsWorktreePath(ctx, repoRoot, syncBranch)

	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)

	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}

	// Get remote name
	remote := getRemoteForBranch(ctx, worktreePath, syncBranch)

	// Fetch from remote to get latest state
	fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, syncBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch failed: %w\n%s", err, output)
	}

	// Reset worktree to remote's state
	resetCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "reset", "--hard",
		fmt.Sprintf("%s/%s", remote, syncBranch))
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset failed: %w\n%s", err, output)
	}

	// Convert absolute path to relative path from repo root
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to get relative JSONL path: %w", err)
	}

	// Copy JSONL from worktree to main repo
	if err := copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath); err != nil {
		return err
	}

	return nil
}

// performContentMerge extracts JSONL from base, local, and remote, then merges content.
// Returns the merged JSONL content.
func performContentMerge(ctx context.Context, worktreePath, branch, remote, jsonlRelPath string) ([]byte, error) {
	// Find merge base
	mergeBaseCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge-base",
		"HEAD", fmt.Sprintf("%s/%s", remote, branch))
	mergeBaseOutput, err := mergeBaseCmd.Output()
	if err != nil {
		// No common ancestor - treat as empty base
		mergeBaseOutput = nil
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))

	// Create temp files for 3-way merge
	tmpDir, err := os.MkdirTemp("", "bd-merge-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	baseFile := filepath.Join(tmpDir, "base.jsonl")
	localFile := filepath.Join(tmpDir, "local.jsonl")
	remoteFile := filepath.Join(tmpDir, "remote.jsonl")
	outputFile := filepath.Join(tmpDir, "merged.jsonl")

	// Extract base JSONL (may not exist if this is first divergence)
	if mergeBase != "" {
		baseContent, err := extractJSONLFromCommit(ctx, worktreePath, mergeBase, jsonlRelPath)
		if err != nil {
			// Base file might not exist in ancestor - use empty file
			baseContent = []byte{}
		}
		if err := os.WriteFile(baseFile, baseContent, 0600); err != nil {
			return nil, fmt.Errorf("failed to write base file: %w", err)
		}
	} else {
		// No merge base - use empty file
		if err := os.WriteFile(baseFile, []byte{}, 0600); err != nil {
			return nil, fmt.Errorf("failed to write empty base file: %w", err)
		}
	}

	// Extract local JSONL (current HEAD in worktree)
	localContent, err := extractJSONLFromCommit(ctx, worktreePath, "HEAD", jsonlRelPath)
	if err != nil {
		// Local file might not exist - use empty
		localContent = []byte{}
	}
	if err := os.WriteFile(localFile, localContent, 0600); err != nil {
		return nil, fmt.Errorf("failed to write local file: %w", err)
	}

	// Extract remote JSONL
	remoteRef := fmt.Sprintf("%s/%s", remote, branch)
	remoteContent, err := extractJSONLFromCommit(ctx, worktreePath, remoteRef, jsonlRelPath)
	if err != nil {
		// Remote file might not exist - use empty
		remoteContent = []byte{}
	}
	if err := os.WriteFile(remoteFile, remoteContent, 0600); err != nil {
		return nil, fmt.Errorf("failed to write remote file: %w", err)
	}

	// Perform 3-way merge using bd's merge algorithm
	// The merge function writes to outputFile (first arg) and returns error if conflicts
	err = merge.Merge3Way(outputFile, baseFile, localFile, remoteFile, false)
	if err != nil {
		// Check if it's a conflict error
		if strings.Contains(err.Error(), "merge completed with") {
			// There were conflicts - this is rare for JSONL since most fields can be
			// auto-merged. When it happens, it means both sides changed the same field
			// to different values. We fail here rather than writing corrupt JSONL.
			return nil, fmt.Errorf("merge conflict: %w (manual resolution required)", err)
		}
		return nil, fmt.Errorf("3-way merge failed: %w", err)
	}

	// Read merged result
	mergedContent, err := os.ReadFile(outputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read merged file: %w", err)
	}

	return mergedContent, nil
}

// extractJSONLFromCommit extracts a file's content from a specific git commit.
func extractJSONLFromCommit(ctx context.Context, worktreePath, commit, filePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "show",
		fmt.Sprintf("%s:%s", commit, filePath))
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to extract %s from %s: %w", filePath, commit, err)
	}
	return output, nil
}

// copyJSONLToMainRepo copies JSONL and related files from worktree to main repo.
func copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath string) error {
	// GH#785: Handle bare repo worktrees where jsonlRelPath might include the
	// worktree name (e.g., "main/.beads/issues.jsonl" instead of ".beads/issues.jsonl").
	// The sync branch uses sparse checkout for .beads/* so we normalize the path
	// to strip any leading components before .beads.
	normalizedRelPath := normalizeBeadsRelPath(jsonlRelPath)
	worktreeJSONLPath := filepath.Join(worktreePath, normalizedRelPath)

	// Check if worktree JSONL exists
	if _, err := os.Stat(worktreeJSONLPath); os.IsNotExist(err) {
		// No JSONL in worktree yet, nothing to sync
		return nil
	}

	// Copy JSONL from worktree to main repo
	data, err := os.ReadFile(worktreeJSONLPath)
	if err != nil {
		return fmt.Errorf("failed to read worktree JSONL: %w", err)
	}

	if err := os.WriteFile(jsonlPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write main JSONL: %w", err)
	}

	// Also sync other beads files back (metadata.json)
	beadsDir := filepath.Dir(jsonlPath)
	worktreeBeadsDir := filepath.Dir(worktreeJSONLPath)
	for _, filename := range []string{"metadata.json"} {
		worktreeSrcPath := filepath.Join(worktreeBeadsDir, filename)
		if fileData, err := os.ReadFile(worktreeSrcPath); err == nil {
			dstPath := filepath.Join(beadsDir, filename)
			_ = os.WriteFile(dstPath, fileData, 0600) // Best effort, match JSONL permissions
		}
	}

	return nil
}

// hasChangesInWorktree checks if there are uncommitted changes in the worktree
func hasChangesInWorktree(ctx context.Context, worktreePath, filePath string) (bool, error) {
	// Check the entire .beads directory for changes
	beadsDir := filepath.Dir(filePath)
	relBeadsDir, err := filepath.Rel(worktreePath, beadsDir)
	if err != nil {
		// Fallback to checking just the file
		relPath, err := filepath.Rel(worktreePath, filePath)
		if err != nil {
			return false, fmt.Errorf("failed to make path relative: %w", err)
		}
		cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain", relPath)
		output, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("git status failed in worktree: %w", err)
		}
		return len(strings.TrimSpace(string(output))) > 0, nil
	}

	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain", relBeadsDir)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed in worktree: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// commitInWorktree stages and commits changes in the worktree
func commitInWorktree(ctx context.Context, worktreePath, jsonlRelPath, message string) error {
	// Stage the entire .beads directory
	beadsRelDir := filepath.Dir(jsonlRelPath)

	addCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", beadsRelDir)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed in worktree: %w", err)
	}

	// Commit with --no-verify to skip hooks (pre-commit hook would fail in worktree context)
	// The worktree is internal to bd sync, so we don't need to run bd's pre-commit hook
	commitCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "commit", "--no-verify", "-m", message)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed in worktree: %w\n%s", err, output)
	}

	return nil
}

// isNonFastForwardError checks if git push output indicates a non-fast-forward rejection
func isNonFastForwardError(output string) bool {
	// Git outputs these messages for non-fast-forward rejections
	return strings.Contains(output, "non-fast-forward") ||
		strings.Contains(output, "fetch first") ||
		strings.Contains(output, "rejected") && strings.Contains(output, "behind")
}

// contentMergeRecovery performs a content-level merge when push fails due to divergence.
//
// The problem with git rebase: it replays commits textually, which can resurrect
// tombstones. For example, if remote has a tombstone and local has 'closed',
// the rebase overwrites the tombstone with 'closed'.
//
// This function uses the same content-level merge as PullFromSyncBranch:
// 1. Fetch remote
// 2. Find merge base
// 3. Extract JSONL from base, local, remote
// 4. Run 3-way content merge (respects tombstones)
// 5. Reset to remote, commit merged content
//
// This fixes a sync race where rebase-based divergence recovery resurrects tombstones.
func contentMergeRecovery(ctx context.Context, worktreePath, branch, remote string) error {
	// The JSONL is always at .beads/issues.jsonl relative to worktree
	jsonlRelPath := filepath.Join(".beads", "issues.jsonl")

	// Step 1: Fetch latest from remote
	fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, branch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fetch failed: %w\n%s", err, output)
	}

	// Step 2: Perform content-level merge (same algorithm as PullFromSyncBranch)
	mergedContent, err := performContentMerge(ctx, worktreePath, branch, remote, jsonlRelPath)
	if err != nil {
		return fmt.Errorf("content merge failed: %w", err)
	}

	// Step 3: Reset worktree to remote's history (adopt their commit graph)
	resetCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "reset", "--hard",
		fmt.Sprintf("%s/%s", remote, branch))
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset failed: %w\n%s", err, output)
	}

	// Step 4: Write merged content
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	if err := os.MkdirAll(filepath.Dir(worktreeJSONLPath), 0750); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(worktreeJSONLPath, mergedContent, 0600); err != nil {
		return fmt.Errorf("failed to write merged JSONL: %w", err)
	}

	// Step 5: Check if merge produced any changes from remote
	hasChanges, err := hasChangesInWorktree(ctx, worktreePath, worktreeJSONLPath)
	if err != nil {
		return fmt.Errorf("failed to check for changes: %w", err)
	}

	// Step 6: Commit merged content if there are changes
	if hasChanges {
		message := "bd sync: merge divergent histories (content-level recovery)"
		if err := commitInWorktree(ctx, worktreePath, jsonlRelPath, message); err != nil {
			return fmt.Errorf("failed to commit merged content: %w", err)
		}
	}

	return nil
}


// runCmdWithTimeoutMessage runs a command and prints a helpful message if it takes too long.
// This helps when git operations hang waiting for credential/browser auth.
//
// Parameters:
//   - ctx: Context for cancellation
//   - timeoutMsg: Message to print when timeout is reached (e.g., "Waiting for Git authentication in browser...")
//   - timeoutDelay: Duration to wait before printing message (e.g., 5 seconds)
//   - cmd: The command to run
//
// Returns: combined output and error from the command
func runCmdWithTimeoutMessage(ctx context.Context, timeoutMsg string, timeoutDelay time.Duration, cmd *exec.Cmd) ([]byte, error) {
	// Use done channel to cleanly exit goroutine when command completes
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(timeoutDelay):
			fmt.Fprintf(os.Stderr, "⏳ %s\n", timeoutMsg)
		case <-done:
			// Command completed, exit cleanly
		case <-ctx.Done():
			// Context canceled, don't print message
		}
	}()

	output, err := cmd.CombinedOutput()
	close(done)
	return output, err
}

// pushFromWorktree pushes the sync branch from the worktree with retry logic
// for handling concurrent push conflicts (non-fast-forward errors).
func pushFromWorktree(ctx context.Context, worktreePath, branch string) error {
	remote := getRemoteForBranch(ctx, worktreePath, branch)
	maxRetries := 5

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Push with explicit remote and branch, set upstream if not set
		cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "--set-upstream", remote, branch)
		// Set BD_SYNC_IN_PROGRESS so pre-push hook knows to skip checks (GH#532)
		// This prevents circular error where hook suggests running bd sync
		cmd.Env = append(os.Environ(), "BD_SYNC_IN_PROGRESS=1")

		// Run with timeout message in case of hanging auth
		output, err := runCmdWithTimeoutMessage(
			ctx,
			fmt.Sprintf("Git push is waiting (possibly for authentication). If this hangs, check for a browser auth prompt."),
			5*time.Second,
			cmd,
		)

		if err == nil {
			return nil // Success
		}

		outputStr := string(output)
		lastErr = fmt.Errorf("git push failed from worktree: %w\n%s", err, outputStr)

		// Check if this is a non-fast-forward error (concurrent push conflict)
		if isNonFastForwardError(outputStr) {
			// Use content-level merge instead of git rebase.
			// Git rebase is text-level and can resurrect tombstones.
			if mergeErr := contentMergeRecovery(ctx, worktreePath, branch, remote); mergeErr != nil {
				// Content merge failed - provide clear recovery options
				return fmt.Errorf(`sync branch diverged and automatic recovery failed

The sync branch '%s' has diverged from remote '%s/%s' and automatic content merge failed.

Recovery options:
  1. Reset to remote state (discard local sync changes):
     bd sync --reset-remote

  2. Force push local state to remote (overwrites remote):
     bd sync --force-push

  3. Manual recovery in the sync branch worktree:
     cd .git/beads-worktrees/%s
     git status
     # Resolve conflicts manually, then:
     bd sync

Original error: %v
Merge error: %v`, branch, remote, branch, branch, lastErr, mergeErr)
			}
			// Content merge succeeded - retry push immediately (no backoff needed)
			continue
		}

		// For other errors, use exponential backoff before retry
		if attempt < maxRetries-1 {
			waitTime := time.Duration(100<<uint(attempt)) * time.Millisecond // 100ms, 200ms, 400ms, 800ms
			time.Sleep(waitTime)
		}
	}

	return fmt.Errorf("push failed after %d attempts: %w", maxRetries, lastErr)
}

// PushSyncBranch pushes the sync branch to remote.
// This is used after confirmation when sync.require_confirmation_on_mass_delete is enabled
// and a mass deletion was detected during merge.
//
// Parameters:
//   - ctx: Context for cancellation
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//
// Returns error if push fails.
func PushSyncBranch(ctx context.Context, repoRoot, syncBranch string) error {
	// Worktree path is under .git/beads-worktrees/<branch>
	worktreePath := filepath.Join(repoRoot, ".git", "beads-worktrees", syncBranch)

	// Recreate worktree if it was cleaned up, using the same pattern as CommitToSyncBranch
	wtMgr := git.NewWorktreeManager(repoRoot)
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return fmt.Errorf("failed to ensure worktree exists: %w", err)
	}

	return pushFromWorktree(ctx, worktreePath, syncBranch)
}

// getBeadsWorktreePath returns the path where beads worktrees should be stored.
// GH#639: Uses git rev-parse --git-common-dir to correctly handle bare repos and worktrees.
// For regular repos, this is typically .git/beads-worktrees/<branch>.
// For bare repos or worktrees of bare repos, this uses the common git directory.
func getBeadsWorktreePath(ctx context.Context, repoRoot, syncBranch string) string {
	// Try to get the git common directory using git's native API
	// This handles all cases: regular repos, worktrees, bare repos
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err == nil {
		gitCommonDir := strings.TrimSpace(string(output))
		// Make path absolute if it's relative
		if !filepath.IsAbs(gitCommonDir) {
			gitCommonDir = filepath.Join(repoRoot, gitCommonDir)
		}
		return filepath.Join(gitCommonDir, "beads-worktrees", syncBranch)
	}

	// Fallback to legacy behavior for compatibility
	return filepath.Join(repoRoot, ".git", "beads-worktrees", syncBranch)
}

// getRemoteForBranch gets the remote name for a branch, defaulting to "origin"
func getRemoteForBranch(ctx context.Context, worktreePath, branch string) string {
	remoteCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "config", "--get", fmt.Sprintf("branch.%s.remote", branch))
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		return "origin" // Default
	}
	return strings.TrimSpace(string(remoteOutput))
}

// GetRepoRoot returns the git repository root directory
// For worktrees, this returns the main repository root (not the worktree root)
func GetRepoRoot(ctx context.Context) (string, error) {
	// Check if .git is a file (worktree) or directory (regular repo)
	gitPath := ".git"
	if info, err := os.Stat(gitPath); err == nil {
		if info.Mode().IsRegular() {
			// Worktree: read .git file
			content, err := os.ReadFile(gitPath)
			if err != nil {
				return "", fmt.Errorf("failed to read .git file: %w", err)
			}
			line := strings.TrimSpace(string(content))
			if strings.HasPrefix(line, "gitdir: ") {
				gitDir := strings.TrimPrefix(line, "gitdir: ")
				// Remove /worktrees/* part
				if idx := strings.Index(gitDir, "/worktrees/"); idx > 0 {
					gitDir = gitDir[:idx]
				}
				return filepath.Dir(gitDir), nil
			}
		} else if info.IsDir() {
			// Regular repo: .git is a directory
			absGitPath, err := filepath.Abs(gitPath)
			if err != nil {
				return "", err
			}
			return filepath.Dir(absGitPath), nil
		}
	}

	// Fallback to git command
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// countIssuesInContent counts the number of non-empty lines in JSONL content.
// Each non-empty line represents one issue. Used for safety checks.
func countIssuesInContent(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// issueSummary holds minimal issue info for forensic logging
type issueSummary struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// parseIssuesFromContent extracts issue IDs and titles from JSONL content.
// Used for forensic logging of vanished issues.
func parseIssuesFromContent(content []byte) map[string]issueSummary {
	result := make(map[string]issueSummary)
	if len(content) == 0 {
		return result
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var summary issueSummary
		if err := json.Unmarshal([]byte(line), &summary); err != nil {
			continue // Skip malformed lines
		}
		if summary.ID != "" {
			result[summary.ID] = summary
		}
	}
	return result
}

// formatVanishedIssues returns forensic info lines when issues vanish during merge (bd-lsa, bd-7z4).
// Returns string slices for caller to display as appropriate for their output format.
func formatVanishedIssues(localIssues, mergedIssues map[string]issueSummary, localCount, mergedCount int) []string {
	var lines []string
	timestamp := time.Now().Format("2006-01-02 15:04:05 MST")

	lines = append(lines, fmt.Sprintf("\n📋 Mass deletion forensic log [%s]", timestamp))
	lines = append(lines, fmt.Sprintf("   Before merge: %d issues", localCount))
	lines = append(lines, fmt.Sprintf("   After merge:  %d issues", mergedCount))
	lines = append(lines, "   Vanished issues:")

	// Collect vanished IDs first, then sort for deterministic output
	var vanishedIDs []string
	for id := range localIssues {
		if _, exists := mergedIssues[id]; !exists {
			vanishedIDs = append(vanishedIDs, id)
		}
	}
	sort.Strings(vanishedIDs)

	for _, id := range vanishedIDs {
		title := localIssues[id].Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		lines = append(lines, fmt.Sprintf("     - %s: %s", id, title))
	}
	lines = append(lines, fmt.Sprintf("   Total vanished: %d\n", len(vanishedIDs)))

	return lines
}

// normalizeBeadsRelPath strips any leading path components before .beads/.
// This handles bare repo worktrees where the relative path includes the worktree
// name (e.g., "main/.beads/issues.jsonl" -> ".beads/issues.jsonl").
// GH#785: Fix for sync failing across worktrees in bare repo setup.
func normalizeBeadsRelPath(relPath string) string {
	// Use filepath.ToSlash for consistent handling across platforms
	normalized := filepath.ToSlash(relPath)
	// Look for ".beads/" to ensure we match the directory, not a prefix like ".beads-backup"
	if idx := strings.Index(normalized, ".beads/"); idx > 0 {
		// Strip leading path components before .beads
		return filepath.FromSlash(normalized[idx:])
	}
	return relPath
}

// HasGitRemote checks if any git remote exists
func HasGitRemote(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "git", "remote")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// GetCurrentBranch returns the name of the current git branch
func GetCurrentBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// IsSyncBranchSameAsCurrent returns true if the sync branch is the same as the current branch.
// This is used to detect the case where we can't use a worktree because the branch is already
// checked out. In this case, we should commit directly to the current branch instead.
// See: https://github.com/steveyegge/beads/issues/519
func IsSyncBranchSameAsCurrent(ctx context.Context, syncBranch string) bool {
	currentBranch, err := GetCurrentBranch(ctx)
	if err != nil {
		return false
	}
	return currentBranch == syncBranch
}
